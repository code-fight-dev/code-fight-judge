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
	repo             *storage.JobsRepo
	runner           *runner.Service
	callbackClient   *callback.Client
	pollInterval     time.Duration
	callbackAttempts int
	callbackBackoff  time.Duration
}

func NewWorker(
	repo *storage.JobsRepo,
	runner *runner.Service,
	callbackClient *callback.Client,
	pollInterval time.Duration,
	callbackAttempts int,
	callbackBackoff time.Duration,
) *Worker {
	if callbackAttempts <= 0 {
		callbackAttempts = 1
	}
	if callbackBackoff <= 0 {
		callbackBackoff = time.Second
	}

	return &Worker{
		repo:             repo,
		runner:           runner,
		callbackClient:   callbackClient,
		pollInterval:     pollInterval,
		callbackAttempts: callbackAttempts,
		callbackBackoff:  callbackBackoff,
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
	if err := w.sendResultWithRetry(ctx, job.CallbackURL, job.CallbackToken, callback.ResultRequest{
		JudgeSubmissionID: job.ID,
		SubmissionID:      job.SubmissionID,
		JudgeStatus:       "running",
		StartedAt:         &startedAt,
		Tests:             []callback.TestResult{},
	}); err != nil {
		log.Printf("worker running callback failed job id=%s error=%v", job.ID, err)
	}

	result, runErr := w.runner.Run(ctx, job)

	finishedAt := time.Now().UTC()

	if runErr != nil {
		log.Printf("worker run failed job id=%s error=%v", job.ID, runErr)

		callbackErr := w.sendResultWithRetry(ctx, job.CallbackURL, job.CallbackToken, callback.ResultRequest{
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
			PeakMemoryKb:  nil,
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

	callbackErr := w.sendResultWithRetry(ctx, job.CallbackURL, job.CallbackToken, callback.ResultRequest{
		JudgeSubmissionID: job.ID,
		SubmissionID:      job.SubmissionID,
		JudgeStatus:       "finished",

		Verdict:      stringPtr(result.Verdict),
		ErrorMessage: nullableString(result.ErrorMessage),

		PassedTests: result.PassedTests,
		TotalTests:  result.TotalTests,
		Score:       result.Score,

		CompileTimeMs: intPtr(result.CompileTimeMS),
		RunTimeMs:     intPtr(result.RunTimeMS),
		PeakMemoryKb:  nullableInt(result.PeakMemoryKB),
		ExitCode:      intPtr(result.ExitCode),

		CompileLog:      stringPtr(""),
		StdoutTruncated: stringPtr(truncateString(result.Stdout, job.Limits.OutputKB*1024)),
		StderrTruncated: stringPtr(truncateString(result.Stderr, job.Limits.OutputKB*1024)),

		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		Tests:      toCallbackTests(result.TestResults),
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

func (w *Worker) sendResultWithRetry(
	ctx context.Context,
	callbackURL string,
	callbackToken string,
	payload callback.ResultRequest,
) error {
	var lastErr error

	for attempt := 1; attempt <= w.callbackAttempts; attempt++ {
		if err := w.callbackClient.SendResult(ctx, callbackURL, callbackToken, payload); err != nil {
			lastErr = err
			log.Printf(
				"callback attempt failed job_id=%s submission_id=%s attempt=%d/%d err=%v",
				payload.JudgeSubmissionID,
				payload.SubmissionID,
				attempt,
				w.callbackAttempts,
				err,
			)
		} else {
			return nil
		}

		if attempt == w.callbackAttempts {
			break
		}

		delay := w.callbackBackoff * time.Duration(attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	return lastErr
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

func nullableInt(v int) *int {
	if v <= 0 {
		return nil
	}

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
			MemoryKb:       nullableInt(r.MemoryKB),
			ExitCode:       intPtr(r.ExitCode),
			CheckerMessage: nullableString(r.CheckerMessage),
		})
	}

	return out
}
