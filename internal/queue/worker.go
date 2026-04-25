package queue

import (
	"context"
	"fmt"
	"log"
	"time"

	"code-fight-judge/internal/callback"
	"code-fight-judge/internal/runner"
	"code-fight-judge/internal/storage"
)

type Worker struct {
	repo           *storage.JobsRepo
	runner         *runner.Service
	callbackClient *callback.Client
	pollInterval   time.Duration
}

func NewWorker(
	repo *storage.JobsRepo,
	runner *runner.Service,
	callbackClient *callback.Client,
	pollInterval time.Duration,
) *Worker {
	return &Worker{
		repo:           repo,
		runner:         runner,
		callbackClient: callbackClient,
		pollInterval:   pollInterval,
	}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			if err := w.processOne(ctx); err != nil {
				log.Printf("worker process job error: %v", err)
			}
		}
	}
}

func (w *Worker) processOne(ctx context.Context) error {
	job, err := w.repo.ClaimNextQueued(ctx)
	if err != nil {
		return fmt.Errorf("claim next queued job: %w", err)
	}
	if job == nil {
		return nil
	}

	log.Printf("worker picked job id=%s submission_id=%s", job.ID, job.SubmissionID)

	startedAt := time.Now().UTC()

	result, runErr := w.runner.Run(ctx, job)

	finishedAt := time.Now().UTC()

	if runErr != nil {
		log.Printf("worker run failed job id=%s error=%v", job.ID, runErr)

		callbackErr := w.callbackClient.SendResult(ctx, job.CallbackURL, job.CallbackToken, callback.ResultRequest{
			JudgeSubmissionID: job.ID,
			SubmissionID:      job.SubmissionID,
			JudgeStatus:       "finished",

			Verdict:      stringPtr("system_error"),
			ErrorMessage: stringPtr(runErr.Error()),

			PassedTests: 0,
			TotalTests:  len(job.Tests),
			Score:       0,

			CompileTimeMs: intPtr(0),
			RunTimeMs:     intPtr(0),
			PeakMemoryKb:  intPtr(0),
			ExitCode:      intPtr(-1),

			CompileLog:      stringPtr(""),
			StdoutTruncated: stringPtr(""),
			StderrTruncated: stringPtr(""),

			StartedAt:  &startedAt,
			FinishedAt: &finishedAt,
			Tests:      []callback.TestResult{},
		})
		if callbackErr != nil {
			_ = w.repo.MarkFailed(ctx, job.ID)
			return fmt.Errorf("send internal error callback: %w", callbackErr)
		}

		if err := w.repo.MarkFailed(ctx, job.ID); err != nil {
			return fmt.Errorf("mark job failed: %w", err)
		}

		return nil
	}

	callbackErr := w.callbackClient.SendResult(ctx, job.CallbackURL, job.CallbackToken, callback.ResultRequest{
		JudgeSubmissionID: job.ID,
		SubmissionID:      job.SubmissionID,
		JudgeStatus:       "finished",

		Verdict:      stringPtr(result.Verdict),
		ErrorMessage: nullableString(result.ErrorMessage),

		PassedTests: result.PassedTests,
		TotalTests:  result.TotalTests,
		Score:       result.Score,

		CompileTimeMs: intPtr(result.CompileTimeMS),
		RunTimeMs: intPtr(result.RunTimeMS),
		PeakMemoryKb:  intPtr(0),
		ExitCode:      intPtr(result.ExitCode),

		CompileLog:      stringPtr(""),
		StdoutTruncated: stringPtr(truncateString(result.Stdout, job.Limits.OutputKB*1024)),
		StderrTruncated: stringPtr(truncateString(result.Stderr, job.Limits.OutputKB*1024)),

		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		Tests: toCallbackTests(result.TestResults),
	})
	if callbackErr != nil {
		_ = w.repo.MarkFailed(ctx, job.ID)
		return fmt.Errorf("send callback: %w", callbackErr)
	}

	if err := w.repo.MarkFinished(ctx, job.ID); err != nil {
		return fmt.Errorf("mark job finished: %w", err)
	}

	log.Printf(
		"worker finished job id=%s verdict=%s passed=%d/%d score=%d",
		job.ID,
		result.Verdict,
		result.PassedTests,
		result.TotalTests,
		result.Score,
	)

	return nil
}

func truncateString(value string, limit int) string {
	if limit <= 0 {
		return ""
	}

	if len(value) <= limit {
		return value
	}

	return value[:limit]
}

func stringPtr(v string) *string {
	return &v
}

func intPtr(v int) *int {
	return &v
}

func int64ToIntPtr(v int64) *int {
	n := int(v)
	return &n
}

func nullableString(v string) *string {
	if v == "" {
		return nil
	}

	return &v
}

func toCallbackTests(results []runner.TestResult) []callback.TestResult {
	out := make([]callback.TestResult, 0, len(results))

	for _, r := range results {
		out = append(out, callback.TestResult{
			TestCaseID:     r.TestCaseID,
			Status:         r.Status,
			TimeMs:         intPtr(r.TimeMS),
			MemoryKb:       intPtr(r.MemoryKB),
			ExitCode:       intPtr(r.ExitCode),
			CheckerMessage: nullableString(r.CheckerMessage),
		})
	}

	return out
}