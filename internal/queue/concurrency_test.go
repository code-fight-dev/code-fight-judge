package queue

import (
	"context"
	"testing"
	"time"

	"code-fight-judge/internal/callback"
	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/runner"
)

func TestWorkerBoundsConcurrentExecutions(t *testing.T) {
	queue := make(chan *jobs.Job, 3)
	for _, id := range []string{"first", "second", "third"} {
		job := testJob()
		job.ID = id
		queue <- job
	}
	started := make(chan string, 3)
	release := make(chan struct{}, 1)
	run := gatedRunner{started: started, release: release}
	worker := NewWorker(concurrentTestStore{queue: queue}, run, discardCallback{}, testCallbackTarget,
		nil, time.Millisecond, 1, time.Second, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("worker pool did not stop after cancellation")
		}
	}()

	nextStarted := func() string {
		t.Helper()
		select {
		case id := <-started:
			return id
		case <-ctx.Done():
			t.Fatal("timed out waiting for an execution to start")
			return ""
		}
	}
	first, second := nextStarted(), nextStarted()
	if first == second {
		t.Fatalf("same job started twice: %s", first)
	}
	select {
	case id := <-started:
		t.Fatalf("job %s started while both workers were busy", id)
	case <-time.After(50 * time.Millisecond):
	}

	release <- struct{}{}
	if third := nextStarted(); third == first || third == second {
		t.Fatalf("released slot ran a job that had already started: %s", third)
	}
}

type concurrentTestStore struct {
	JobsStore
	queue <-chan *jobs.Job
}

func (s concurrentTestStore) ClaimNextQueued(context.Context) (*jobs.Job, error) {
	select {
	case job := <-s.queue:
		return job, nil
	default:
		return nil, nil
	}
}

func (concurrentTestStore) ClaimResult(context.Context, time.Duration) (*callback.Delivery, error) {
	return nil, nil
}

func (concurrentTestStore) RenewLease(context.Context, string, *time.Time) error {
	return nil
}

func (concurrentTestStore) SaveResult(context.Context, jobs.Status, callback.ResultRequest) error {
	return nil
}

type gatedRunner struct {
	started chan<- string
	release <-chan struct{}
}

func (r gatedRunner) Run(ctx context.Context, job *jobs.Job) (*runner.RunResult, error) {
	r.started <- job.ID
	select {
	case <-r.release:
		return &runner.RunResult{Verdict: "accepted"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type discardCallback struct{}

func (discardCallback) SendResult(context.Context, string, string, callback.ResultRequest) error {
	return nil
}
