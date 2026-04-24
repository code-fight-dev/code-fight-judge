package queue

import (
	"context"
	"log"
	"time"

	"code-fight-judge/internal/runner"
	"code-fight-judge/internal/storage"
)

type Worker struct {
	repo          *storage.JobsRepo
	runner        *runner.Service
	pollInterval  time.Duration
}

func NewWorker(repo *storage.JobsRepo, runner *runner.Service, pollInterval time.Duration) *Worker {
	return &Worker{
		repo:         repo,
		runner:       runner,
		pollInterval: pollInterval,
	}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		if err := w.processOne(ctx); err != nil {
			log.Printf("worker process error: %v", err)
		}

		select {
		case <-ctx.Done():
			log.Printf("worker stopped")
			return
		case <-ticker.C:
		default:
			time.Sleep(300 * time.Millisecond)
		}
	}
}

func (w *Worker) processOne(ctx context.Context) error {
	job, err := w.repo.ClaimNextQueued(ctx)
	if err != nil {
		return err
	}
	if job == nil {
		return nil
	}

	log.Printf("worker picked job id=%s submission_id=%s", job.ID, job.SubmissionID)

	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := w.runner.Run(runCtx, job); err != nil {
		log.Printf("worker run failed job id=%s: %v", job.ID, err)
		if markErr := w.repo.MarkFailed(ctx, job.ID); markErr != nil {
			log.Printf("worker mark failed error job id=%s: %v", job.ID, markErr)
		}
		return nil
	}

	if err := w.repo.MarkFinished(ctx, job.ID); err != nil {
		log.Printf("worker mark finished error job id=%s: %v", job.ID, err)
		return nil
	}

	log.Printf("worker finished job id=%s", job.ID)
	return nil
}