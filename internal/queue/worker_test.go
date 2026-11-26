package queue

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"code-fight-judge/internal/callback"
	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/runner"
)

type fakeJobsStore struct {
	job         *jobs.Job
	claimErr    error
	result      *callback.Delivery
	saved       jobs.Status
	attempts    int64
	retryAfter  *time.Duration
	renewErr    error
	renewErrors []error
}

func (s *fakeJobsStore) ClaimNextQueued(ctx context.Context) (*jobs.Job, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}

	job := s.job
	s.job = nil
	return job, nil
}

func (s *fakeJobsStore) SaveResult(_ context.Context, status jobs.Status, payload callback.ResultRequest) error {
	s.saved = status
	s.result = &callback.Delivery{Payload: payload}
	return nil
}

func (s *fakeJobsStore) ClaimResult(_ context.Context, _ time.Duration) (*callback.Delivery, error) {
	if s.result != nil {
		s.attempts++
		s.result.Attempt = s.attempts
	}
	return s.result, nil
}

func (s *fakeJobsStore) FinishResultDelivery(_ context.Context, _ string, _ int64, retryAfter *time.Duration) error {
	s.retryAfter = retryAfter
	if retryAfter == nil {
		s.result = nil
	}
	return nil
}

func (s *fakeJobsStore) RenewLease(ctx context.Context, jobID string, startedAt *time.Time) error {
	if len(s.renewErrors) > 0 {
		err := s.renewErrors[0]
		s.renewErrors = s.renewErrors[1:]
		return err
	}
	return s.renewErr
}

type fakeRunner struct {
	result *runner.RunResult
	err    error
	jobs   []*jobs.Job
}

func (r *fakeRunner) Run(ctx context.Context, job *jobs.Job) (*runner.RunResult, error) {
	r.jobs = append(r.jobs, job)
	if r.err != nil {
		return nil, r.err
	}

	return r.result, nil
}

type callbackCall struct {
	url     string
	token   string
	payload callback.ResultRequest
}

type fakeCallbackClient struct {
	errors []error
	calls  []callbackCall
}

func (c *fakeCallbackClient) SendResult(ctx context.Context, callbackURL string, callbackToken string, payload callback.ResultRequest) error {
	c.calls = append(c.calls, callbackCall{
		url:     callbackURL,
		token:   callbackToken,
		payload: payload,
	})

	if len(c.errors) == 0 {
		return nil
	}

	err := c.errors[0]
	c.errors = c.errors[1:]
	return err
}

type fakeMetrics struct {
	started          int
	finishedStatuses []string
	finishedVerdicts []string
	finishedLanguage []string
	callbackStatuses []string
}

var testCallbackTarget = callback.Target{URL: "https://backend.example.test/callback", Token: "callback-token"}

func newTestWorker(store JobsStore, runner Runner, client CallbackClient, metrics Metrics, poll time.Duration, attempts int, backoff time.Duration) *Worker {
	return NewWorker(store, runner, client, testCallbackTarget, metrics, poll, attempts, backoff, 1)
}

func (m *fakeMetrics) RecordJobStarted() {
	m.started++
}

func (m *fakeMetrics) RecordJobFinished(status string, verdict string, language string, duration time.Duration) {
	m.finishedStatuses = append(m.finishedStatuses, status)
	m.finishedVerdicts = append(m.finishedVerdicts, verdict)
	m.finishedLanguage = append(m.finishedLanguage, language)
}

func (m *fakeMetrics) RecordCallbackAttempt(status string) {
	m.callbackStatuses = append(m.callbackStatuses, status)
}

func TestProcessOneReturnsWhenNoQueuedJob(t *testing.T) {
	store := &fakeJobsStore{}
	runnerSvc := &fakeRunner{}
	callbackClient := &fakeCallbackClient{}
	worker := newTestWorker(store, runnerSvc, callbackClient, nil, time.Second, 1, time.Second)

	err := worker.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne() error = %v", err)
	}
	if len(runnerSvc.jobs) != 0 {
		t.Fatalf("runner calls = %d, want 0", len(runnerSvc.jobs))
	}
	if len(callbackClient.calls) != 0 {
		t.Fatalf("callback calls = %d, want 0", len(callbackClient.calls))
	}
}

func TestNewWorkerUsesDefaultRetrySettings(t *testing.T) {
	worker := newTestWorker(&fakeJobsStore{}, &fakeRunner{}, &fakeCallbackClient{}, nil, time.Second, 0, 0)

	if worker.callbackAttempts != 1 {
		t.Fatalf("callbackAttempts = %d, want 1", worker.callbackAttempts)
	}
	if worker.callbackBackoff != time.Second {
		t.Fatalf("callbackBackoff = %s, want 1s", worker.callbackBackoff)
	}
}

func TestRunReturnsWhenContextIsCanceled(t *testing.T) {
	worker := newTestWorker(&fakeJobsStore{}, &fakeRunner{}, &fakeCallbackClient{}, nil, time.Hour, 1, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not return after context cancellation")
	}
}

func TestProcessOneRunsJobAndPersistsFinalResult(t *testing.T) {
	job := testJob()
	store := &fakeJobsStore{job: job}
	runnerSvc := &fakeRunner{result: &runner.RunResult{
		ExitCode:      0,
		Stdout:        "abcdef",
		Stderr:        "",
		CompileTimeMS: 11,
		RunTimeMS:     22,
		PeakMemoryKB:  333,
		Verdict:       "accepted",
		PassedTests:   1,
		TotalTests:    1,
		Score:         100,
		TestResults: []runner.TestResult{
			{
				TestCaseID:     "case-1",
				Status:         "accepted",
				TimeMS:         22,
				MemoryKB:       333,
				ExitCode:       0,
				CheckerMessage: "",
			},
		},
	}}
	callbackClient := &fakeCallbackClient{}
	metrics := &fakeMetrics{}
	worker := newTestWorker(store, runnerSvc, callbackClient, metrics, time.Second, 1, time.Second)

	err := worker.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne() error = %v", err)
	}

	if len(runnerSvc.jobs) != 1 || runnerSvc.jobs[0] != job {
		t.Fatalf("runner jobs = %#v, want original job", runnerSvc.jobs)
	}
	if store.saved != jobs.StatusFinished || store.result == nil || store.result.Payload.JudgeSubmissionID != job.ID {
		t.Fatalf("saved status=%s result=%#v", store.saved, store.result)
	}
	if len(callbackClient.calls) != 1 {
		t.Fatalf("callback calls = %d, want one progress update", len(callbackClient.calls))
	}

	running := callbackClient.calls[0]
	if running.payload.JudgeStatus != "running" {
		t.Fatalf("running callback status = %q, want running", running.payload.JudgeStatus)
	}
	if running.url != testCallbackTarget.URL || running.token != testCallbackTarget.Token {
		t.Fatal("running callback did not use the configured target")
	}

	finished := store.result.Payload
	if finished.JudgeStatus != "finished" {
		t.Fatalf("finished callback status = %q, want finished", finished.JudgeStatus)
	}
	if finished.Verdict == nil || *finished.Verdict != "accepted" {
		t.Fatalf("finished verdict = %#v, want accepted", finished.Verdict)
	}
	if finished.PassedTests != 1 || finished.TotalTests != 1 || finished.Score != 100 {
		t.Fatalf("finished score fields = passed %d total %d score %d, want 1/1/100", finished.PassedTests, finished.TotalTests, finished.Score)
	}
	if finished.StdoutTruncated == nil || *finished.StdoutTruncated != "abcdef" {
		t.Fatalf("stdout truncated = %#v, want abcdef", finished.StdoutTruncated)
	}
	if finished.PeakMemoryKb == nil || *finished.PeakMemoryKb != 333 {
		t.Fatalf("peak memory = %#v, want 333", finished.PeakMemoryKb)
	}
	if len(finished.Tests) != 1 || finished.Tests[0].TestCaseID != "case-1" {
		t.Fatalf("finished tests = %#v, want one case-1 result", finished.Tests)
	}
	if metrics.started != 1 {
		t.Fatalf("metrics started = %d, want 1", metrics.started)
	}
	if len(metrics.finishedStatuses) != 1 || metrics.finishedStatuses[0] != "finished" {
		t.Fatalf("metrics finished statuses = %#v, want [finished]", metrics.finishedStatuses)
	}
	if len(metrics.finishedVerdicts) != 1 || metrics.finishedVerdicts[0] != "accepted" {
		t.Fatalf("metrics finished verdicts = %#v, want [accepted]", metrics.finishedVerdicts)
	}
}

func TestProcessOneMarksFailedWhenRunnerReturnsError(t *testing.T) {
	job := testJob()
	store := &fakeJobsStore{job: job}
	runnerSvc := &fakeRunner{err: errors.New("sandbox failed")}
	callbackClient := &fakeCallbackClient{}
	metrics := &fakeMetrics{}
	worker := newTestWorker(store, runnerSvc, callbackClient, metrics, time.Second, 1, time.Second)

	err := worker.processOne(context.Background())
	if err != nil {
		t.Fatalf("processOne() error = %v", err)
	}

	if store.saved != jobs.StatusFailed || store.result == nil || store.result.Payload.JudgeSubmissionID != job.ID {
		t.Fatalf("saved status=%s result=%#v", store.saved, store.result)
	}
	if len(callbackClient.calls) != 1 {
		t.Fatalf("callback calls = %d, want one progress update", len(callbackClient.calls))
	}

	finished := store.result.Payload
	if finished.Verdict == nil || *finished.Verdict != "system_error" {
		t.Fatalf("finished verdict = %#v, want system_error", finished.Verdict)
	}
	if finished.ErrorMessage == nil || *finished.ErrorMessage != "sandbox failed" {
		t.Fatalf("finished error = %#v, want sandbox failed", finished.ErrorMessage)
	}
	if finished.ExitCode == nil || *finished.ExitCode != -1 {
		t.Fatalf("finished exit code = %#v, want -1", finished.ExitCode)
	}
	if len(metrics.finishedStatuses) != 1 || metrics.finishedStatuses[0] != "failed" {
		t.Fatalf("metrics finished statuses = %#v, want [failed]", metrics.finishedStatuses)
	}
	if len(metrics.finishedVerdicts) != 1 || metrics.finishedVerdicts[0] != "system_error" {
		t.Fatalf("metrics finished verdicts = %#v, want [system_error]", metrics.finishedVerdicts)
	}
}

func TestResultDeliveryRetriesStoredVerdictWithoutRunningJobAgain(t *testing.T) {
	job := testJob()
	store := &fakeJobsStore{job: job}
	runnerSvc := &fakeRunner{result: &runner.RunResult{
		Verdict:     "accepted",
		PassedTests: 1,
		TotalTests:  1,
		Score:       100,
	}}
	callbackErr := errors.New("backend unavailable")
	callbackClient := &fakeCallbackClient{errors: []error{nil, callbackErr}}
	worker := newTestWorker(store, runnerSvc, callbackClient, nil, time.Second, 1, time.Second)
	ctx := context.Background()
	if err := worker.processOne(ctx); err != nil {
		t.Fatal(err)
	}
	if processed, err := worker.deliverNextResult(ctx); !processed || !errors.Is(err, callbackErr) {
		t.Fatalf("failed delivery = %t, %v", processed, err)
	}
	if store.retryAfter == nil || *store.retryAfter != time.Second || store.result == nil {
		t.Fatalf("result did not remain pending for retry")
	}
	restarted := newTestWorker(store, runnerSvc, callbackClient, nil, time.Second, 1, time.Second)
	if processed, err := restarted.deliverNextResult(ctx); !processed || err != nil {
		t.Fatalf("retried delivery = %t, %v", processed, err)
	}
	if store.result != nil || len(runnerSvc.jobs) != 1 || len(callbackClient.calls) != 3 {
		t.Fatal("delivery replay should acknowledge one result from one execution")
	}
	if !reflect.DeepEqual(callbackClient.calls[1], callbackClient.calls[2]) {
		t.Fatal("retried callback differs from the first result")
	}
}

func TestSendResultWithRetryRetriesUntilSuccess(t *testing.T) {
	callbackClient := &fakeCallbackClient{
		errors: []error{
			errors.New("first failed"),
			nil,
		},
	}
	metrics := &fakeMetrics{}
	worker := newTestWorker(&fakeJobsStore{}, &fakeRunner{}, callbackClient, metrics, time.Second, 3, time.Nanosecond)

	err := worker.sendResultWithRetry(context.Background(), callback.ResultRequest{
		JudgeSubmissionID: "job-1",
		SubmissionID:      "submission-1",
	})
	if err != nil {
		t.Fatalf("sendResultWithRetry() error = %v", err)
	}
	if len(callbackClient.calls) != 2 {
		t.Fatalf("callback calls = %d, want 2", len(callbackClient.calls))
	}
	if got, want := strings.Join(metrics.callbackStatuses, ","), "error,success"; got != want {
		t.Fatalf("callback metrics = %q, want %q", got, want)
	}
}

func TestSendResultWithRetryReturnsContextErrorDuringBackoff(t *testing.T) {
	callbackClient := &fakeCallbackClient{
		errors: []error{
			errors.New("first failed"),
			errors.New("second failed"),
		},
	}
	worker := newTestWorker(&fakeJobsStore{}, &fakeRunner{}, callbackClient, nil, time.Second, 2, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := worker.sendResultWithRetry(ctx, callback.ResultRequest{
		JudgeSubmissionID: "job-1",
		SubmissionID:      "submission-1",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("sendResultWithRetry() error = %v, want context canceled", err)
	}
	if len(callbackClient.calls) != 1 {
		t.Fatalf("callback calls = %d, want 1", len(callbackClient.calls))
	}
}

func TestHelpers(t *testing.T) {
	if got := truncateString("abcdef", 4); got != "abcd" {
		t.Fatalf("truncateString() = %q, want abcd", got)
	}
	if got := truncateString("abcdef", 0); got != "" {
		t.Fatalf("truncateString(limit 0) = %q, want empty", got)
	}
	if got := nullableInt(0); got != nil {
		t.Fatalf("nullableInt(0) = %#v, want nil", got)
	}
	if got := nullableInt(7); got == nil || *got != 7 {
		t.Fatalf("nullableInt(7) = %#v, want 7", got)
	}
	if got := int64ToIntPtr(64); got == nil || *got != 64 {
		t.Fatalf("int64ToIntPtr(64) = %#v, want 64", got)
	}
	if got := nullableString(""); got != nil {
		t.Fatalf("nullableString(empty) = %#v, want nil", got)
	}
	if got := nullableString("message"); got == nil || *got != "message" {
		t.Fatalf("nullableString(message) = %#v, want message", got)
	}
}

func TestToCallbackTestsMapsRunnerResults(t *testing.T) {
	results := toCallbackTests([]runner.TestResult{
		{
			TestCaseID:     "case-1",
			Status:         "wrong_answer",
			TimeMS:         15,
			MemoryKB:       0,
			ExitCode:       1,
			CheckerMessage: "expected ok",
		},
	})

	if len(results) != 1 {
		t.Fatalf("toCallbackTests len = %d, want 1", len(results))
	}
	if results[0].TestCaseID != "case-1" || results[0].Status != "wrong_answer" {
		t.Fatalf("toCallbackTests result = %#v, want case-1 wrong_answer", results[0])
	}
	if results[0].MemoryKb != nil {
		t.Fatalf("toCallbackTests MemoryKb = %#v, want nil for zero memory", results[0].MemoryKb)
	}
	if results[0].CheckerMessage == nil || *results[0].CheckerMessage != "expected ok" {
		t.Fatalf("toCallbackTests CheckerMessage = %#v, want expected ok", results[0].CheckerMessage)
	}
}

func testJob() *jobs.Job {
	startedAt := time.Now().UTC()
	return &jobs.Job{
		StartedAt:       &startedAt,
		ID:              "job-1",
		SubmissionID:    "submission-1",
		TaskID:          "task-1",
		Language:        "go",
		LanguageVersion: "1.24",
		CallbackURL:     "https://backend.example.test/callback",
		Limits: jobs.Limits{
			TimeMS:   1000,
			MemoryMB: 128,
			OutputKB: 4,
		},
		Tests: []jobs.TestCase{
			{
				ID:             "case-1",
				Input:          "input",
				ExpectedOutput: "output",
				Points:         100,
			},
		},
	}
}

func TestWorkerDoesNotPublishResultAfterLosingLease(t *testing.T) {
	store := &fakeJobsStore{job: testJob(), renewErrors: []error{nil, jobs.ErrLeaseLost}}
	run := &fakeRunner{result: &runner.RunResult{Verdict: "accepted"}}
	callbacks := &fakeCallbackClient{}
	worker := newTestWorker(store, run, callbacks, nil, time.Second, 1, time.Millisecond)
	if err := worker.processOne(context.Background()); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("error=%v", err)
	}
	if len(callbacks.calls) != 1 || callbacks.calls[0].payload.JudgeStatus != "running" || store.result != nil {
		t.Fatalf("stale result published: %#v", callbacks.calls)
	}
}

type untilCanceledRunner struct{}

func (untilCanceledRunner) Run(ctx context.Context, job *jobs.Job) (*runner.RunResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestLeaseRenewalFailureCancelsRunningJob(t *testing.T) {
	store := &fakeJobsStore{job: testJob(), renewErrors: []error{nil, jobs.ErrLeaseLost}}
	callbacks := &fakeCallbackClient{}
	worker := newTestWorker(store, untilCanceledRunner{}, callbacks, nil, time.Second, 1, time.Millisecond)
	worker.leaseRenewInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := worker.processOne(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if len(callbacks.calls) != 1 || store.result != nil {
		t.Fatal("canceled claim was completed or published")
	}
}

func TestUnavailableTimeIsNotReportedAsZero(t *testing.T) {
	if measuredTime(0, true) != nil {
		t.Fatal("unknown time reported as zero")
	}
	if known := measuredTime(0, false); known == nil || *known != 0 {
		t.Fatal("measured zero lost")
	}
	results := toCallbackTests([]runner.TestResult{{TimeUnavailable: true}})
	if results[0].TimeMs != nil {
		t.Fatal("unknown per-test time reported as zero")
	}
}
