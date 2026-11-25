package queue

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"code-fight-judge/internal/callback"
	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/languages"
	"code-fight-judge/internal/runner"
)

type Worker struct {
	repo               JobsStore
	runner             Runner
	callbackClient     CallbackClient
	callbackTarget     callback.Target
	metrics            Metrics
	pollInterval       time.Duration
	callbackAttempts   int
	callbackBackoff    time.Duration
	leaseRenewInterval time.Duration
	workerCount        int
}

type JobsStore interface {
	ClaimNextQueued(ctx context.Context) (*jobs.Job, error)
	SaveResult(ctx context.Context, status jobs.Status, payload callback.ResultRequest) error
	ClaimResult(ctx context.Context, lease time.Duration) (*callback.Delivery, error)
	FinishResultDelivery(ctx context.Context, jobID string, attempt int64, retryAfter *time.Duration) error
	RenewLease(ctx context.Context, jobID string, startedAt *time.Time) error
}

type Runner interface {
	Run(ctx context.Context, job *jobs.Job) (*runner.RunResult, error)
}

type CallbackClient interface {
	SendResult(ctx context.Context, callbackURL string, callbackToken string, payload callback.ResultRequest) error
}

type Metrics interface {
	RecordJobStarted(language string, queueWait time.Duration)
	RecordJobTimings(language string, compileMS, runMS *int)
	RecordJobFinished(status string, verdict string, language string, duration time.Duration)
	RecordCallbackAttempt(status string)
}

func NewWorker(
	repo JobsStore,
	runner Runner,
	callbackClient CallbackClient,
	callbackTarget callback.Target,
	metrics Metrics,
	pollInterval time.Duration,
	callbackAttempts int,
	callbackBackoff time.Duration,
	workerCount int,
) *Worker {
	if callbackAttempts <= 0 {
		callbackAttempts = 1
	}
	if callbackBackoff <= 0 {
		callbackBackoff = time.Second
	}

	return &Worker{
		repo:               repo,
		runner:             runner,
		callbackClient:     callbackClient,
		callbackTarget:     callbackTarget,
		metrics:            metrics,
		pollInterval:       pollInterval,
		callbackAttempts:   callbackAttempts,
		callbackBackoff:    callbackBackoff,
		leaseRenewInterval: jobs.LeaseDuration / 3,
		workerCount:        max(1, workerCount),
	}
}

func (w *Worker) Run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Go(func() {
		w.deliverResults(ctx)
	})
	for range w.workerCount {
		workers.Go(func() {
			w.runJobs(ctx)
		})
	}
	workers.Wait()
}

func (w *Worker) runJobs(ctx context.Context) {
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
	if err := w.verifyLease(ctx, job); err != nil {
		return fmt.Errorf("verify claimed job lease: %w", err)
	}
	jobCtx, cancelJob := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go w.keepLease(jobCtx, cancelJob, job, heartbeatDone)
	defer func() { cancelJob(); <-heartbeatDone }()
	ctx = jobCtx

	log.Printf("worker picked job id=%s submission_id=%s", job.ID, job.SubmissionID)

	startedAt := *job.StartedAt
	language := languages.NormalizeLanguage(job.Language)
	status := "failed"
	verdict := "system_error"
	executionDuration := -time.Nanosecond // no trusted duration for an infrastructure failure
	if w.metrics != nil {
		queueWait := -time.Nanosecond
		if job.FirstAttempt {
			queueWait = max(0, startedAt.Sub(job.CreatedAt))
		}
		w.metrics.RecordJobStarted(language, queueWait)
		defer func() {
			w.metrics.RecordJobFinished(status, verdict, language, executionDuration)
		}()
	}

	if err := w.sendResultWithRetry(ctx, callback.ResultRequest{
		JudgeSubmissionID: job.ID,
		SubmissionID:      job.SubmissionID,
		JudgeStatus:       "running",
		StartedAt:         &startedAt,
		Tests:             []callback.TestResult{},
	}); err != nil {
		log.Printf("worker running callback failed job id=%s error=%v", job.ID, err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	result, runErr := w.runner.Run(ctx, job)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Fence a stale worker before it saves a final result.
	if err := w.verifyLease(ctx, job); err != nil {
		return fmt.Errorf("verify job lease: %w", err)
	}

	finishedAt := time.Now().UTC()

	if runErr != nil {
		log.Printf("worker run failed job id=%s error=%v", job.ID, runErr)

		return w.repo.SaveResult(ctx, jobs.StatusFailed, callback.ResultRequest{
			JudgeSubmissionID: job.ID,
			SubmissionID:      job.SubmissionID,
			JudgeStatus:       "finished",

			Verdict:      stringPtr("system_error"),
			ErrorMessage: stringPtr(runErr.Error()),

			PassedTests: 0,
			TotalTests:  len(job.Tests),
			Score:       0,

			CompileTimeMs: nil,
			RunTimeMs:     nil,
			PeakMemoryKb:  nil,
			ExitCode:      intPtr(-1),

			CompileLog:      stringPtr(""),
			StdoutTruncated: stringPtr(""),
			StderrTruncated: stringPtr(""),

			StartedAt:  &startedAt,
			FinishedAt: &finishedAt,
			Tests:      []callback.TestResult{},
		})
	}
	verdict = result.Verdict
	if w.metrics != nil {
		var compileMS, runMS *int
		if result.CompileTimeMS > 0 {
			compileMS = measuredTime(result.CompileTimeMS, result.CompileTimeUnavailable)
		}
		if len(result.TestResults) > 0 {
			runMS = measuredTime(result.RunTimeMS, result.RunTimeUnavailable)
		}
		w.metrics.RecordJobTimings(language, compileMS, runMS)
	}
	if !result.CompileTimeUnavailable && !result.RunTimeUnavailable {
		executionDuration = time.Duration(result.DurationMS) * time.Millisecond
	}

	err = w.repo.SaveResult(ctx, jobs.StatusFinished, callback.ResultRequest{
		JudgeSubmissionID: job.ID,
		SubmissionID:      job.SubmissionID,
		JudgeStatus:       "finished",

		Verdict:      stringPtr(result.Verdict),
		ErrorMessage: nullableString(result.ErrorMessage),

		PassedTests: result.PassedTests,
		TotalTests:  result.TotalTests,
		Score:       result.Score,

		CompileTimeMs: measuredTime(result.CompileTimeMS, result.CompileTimeUnavailable),
		RunTimeMs:     measuredTime(result.RunTimeMS, result.RunTimeUnavailable),
		PeakMemoryKb:  nullableInt(result.PeakMemoryKB),
		ExitCode:      intPtr(result.ExitCode),

		CompileLog:      stringPtr(result.CompileLog),
		StdoutTruncated: stringPtr(truncateString(result.Stdout, job.Limits.OutputKB*1024)),
		StderrTruncated: stringPtr(truncateString(result.Stderr, job.Limits.OutputKB*1024)),

		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		Tests:      toCallbackTests(result.TestResults),
	})
	if err != nil {
		return err
	}
	status = "finished"

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

func (w *Worker) verifyLease(ctx context.Context, job *jobs.Job) error {
	if job.StartedAt == nil {
		return jobs.ErrLeaseLost
	}
	leaseCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	return w.repo.RenewLease(leaseCtx, job.ID, job.StartedAt)
}

func (w *Worker) keepLease(ctx context.Context, cancel context.CancelFunc, job *jobs.Job, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(w.leaseRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			err := w.repo.RenewLease(renewCtx, job.ID, job.StartedAt)
			stop()
			if err != nil {
				log.Printf("job lease renewal failed job_id=%s err=%v", job.ID, err)
				cancel()
				return
			}
		}
	}
}

func (w *Worker) sendResultWithRetry(
	ctx context.Context,
	payload callback.ResultRequest,
) error {
	var lastErr error

	for attempt := 1; attempt <= w.callbackAttempts; attempt++ {
		if err := w.sendCallback(ctx, payload); err != nil {
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
			TimeMs:         measuredTime(r.TimeMS, r.TimeUnavailable),
			MemoryKb:       nullableInt(r.MemoryKB),
			ExitCode:       intPtr(r.ExitCode),
			CheckerMessage: nullableString(r.CheckerMessage),
		})
	}

	return out
}

func measuredTime(value int, unavailable bool) *int {
	if unavailable {
		return nil
	}
	return intPtr(value)
}
