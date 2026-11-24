package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code-fight-judge/internal/callback"
	"code-fight-judge/internal/jobs"

	"github.com/jackc/pgx/v5"
)

func (r *JobsRepo) CheckResultSchema(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `SELECT result_payload, callback_next_attempt_at, callback_attempts FROM judge_jobs LIMIT 0`)
	return err
}

func (r *JobsRepo) SaveResult(ctx context.Context, status jobs.Status, payload callback.ResultRequest) error {
	if payload.StartedAt == nil {
		return jobs.ErrLeaseLost
	}
	tag, err := r.pool.Exec(ctx, `UPDATE judge_jobs
		SET status=$2, finished_at=$5, updated_at=clock_timestamp(),
		    result_payload=$6, callback_next_attempt_at=clock_timestamp()
		WHERE id=$1 AND started_at=$3 AND status=$4`,
		payload.JudgeSubmissionID, status, payload.StartedAt, jobs.StatusRunning, payload.FinishedAt, payload)
	if err != nil {
		return fmt.Errorf("save job result: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return jobs.ErrLeaseLost
	}
	return nil
}

func (r *JobsRepo) ClaimResult(ctx context.Context, lease time.Duration) (*callback.Delivery, error) {
	var delivery callback.Delivery
	err := r.pool.QueryRow(ctx, `WITH pending AS (
		SELECT id FROM judge_jobs
		WHERE callback_next_attempt_at <= statement_timestamp()
		ORDER BY callback_next_attempt_at, id
		LIMIT 1 FOR UPDATE SKIP LOCKED
	)
	UPDATE judge_jobs j
	SET callback_next_attempt_at=clock_timestamp()+($1 * interval '1 second'),
	    callback_attempts=j.callback_attempts+1
	FROM pending WHERE j.id=pending.id
	RETURNING j.result_payload, j.callback_attempts`, lease.Seconds()).Scan(&delivery.Payload, &delivery.Attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim job result: %w", err)
	}
	return &delivery, nil
}

func (r *JobsRepo) FinishResultDelivery(ctx context.Context, jobID string, attempt int64, retryAfter *time.Duration) error {
	// A NULL retry delay acknowledges delivery. The attempt number prevents a
	// dispatcher whose claim expired from modifying a newer delivery attempt.
	var delaySeconds *float64
	if retryAfter != nil {
		seconds := retryAfter.Seconds()
		delaySeconds = &seconds
	}
	_, err := r.pool.Exec(ctx, `UPDATE judge_jobs
		SET callback_next_attempt_at=clock_timestamp()+($3 * interval '1 second')
		WHERE id=$1 AND callback_attempts=$2 AND callback_next_attempt_at IS NOT NULL`, jobID, attempt, delaySeconds)
	if err != nil {
		return fmt.Errorf("finish result delivery: %w", err)
	}
	return nil
}
