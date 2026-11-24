package queue

import (
	"context"
	"fmt"
	"log"
	"time"

	"code-fight-judge/internal/callback"
)

const (
	resultSendTimeout = 10 * time.Second
	resultLease       = 20 * time.Second
	maxResultBackoff  = time.Minute
)

func (w *Worker) deliverResults(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		processed, err := w.deliverNextResult(ctx)
		if err != nil && ctx.Err() == nil {
			log.Printf("worker result delivery error: %v", err)
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) deliverNextResult(ctx context.Context) (bool, error) {
	delivery, err := w.repo.ClaimResult(ctx, resultLease)
	if err != nil || delivery == nil {
		return false, err
	}

	sendCtx, cancel := context.WithTimeout(ctx, resultSendTimeout)
	sendErr := w.sendCallback(sendCtx, delivery.Payload)
	cancel()

	var retryAfter *time.Duration
	if sendErr != nil {
		base := min(w.callbackBackoff, maxResultBackoff)
		delay := min(base<<min(delivery.Attempt-1, 6), maxResultBackoff)
		retryAfter = &delay
	}
	jobID := delivery.Payload.JudgeSubmissionID
	if err := w.repo.FinishResultDelivery(ctx, jobID, delivery.Attempt, retryAfter); err != nil {
		return false, err
	}
	if sendErr != nil {
		return true, fmt.Errorf("retry callback for job %s: %w", jobID, sendErr)
	}
	return true, nil
}

func (w *Worker) sendCallback(ctx context.Context, payload callback.ResultRequest) error {
	err := w.callbackClient.SendResult(ctx, w.callbackTarget.URL, w.callbackTarget.Token, payload)
	if w.metrics != nil {
		status := "success"
		if err != nil {
			status = "error"
		}
		w.metrics.RecordCallbackAttempt(status)
	}
	return err
}
