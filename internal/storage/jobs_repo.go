package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"code-fight-judge/internal/jobs"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type JobsRepo struct {
	pool jobsDB
}

type jobsDB interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func NewJobsRepo(pool *pgxpool.Pool) *JobsRepo {
	return &JobsRepo{pool: pool}
}

func (r *JobsRepo) Create(ctx context.Context, req jobs.SubmitRequest) (*jobs.Job, error) {
	now := time.Now().UTC()
	id := uuid.NewString()

	testsJSON, err := json.Marshal(req.Tests)
	if err != nil {
		return nil, fmt.Errorf("marshal tests: %w", err)
	}

	_, err = r.pool.Exec(
		ctx,
		`
		INSERT INTO judge_jobs (
			id,
			submission_id,
			task_id,
			language,
			language_version,
			source_code,
			callback_url,
			callback_token,
			time_limit_ms,
			memory_limit_mb,
			output_limit_kb,
			tests_json,
			status,
			created_at,
			updated_at,
			started_at,
			finished_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13, $14, $15, NULL, NULL
		)
		`,
		id,
		req.SubmissionID,
		req.TaskID,
		req.Language,
		req.LanguageVersion,
		req.SourceCode,
		req.CallbackURL,
		req.CallbackToken,
		req.Limits.TimeMS,
		req.Limits.MemoryMB,
		req.Limits.OutputKB,
		string(testsJSON),
		jobs.StatusQueued,
		now,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert judge job: %w", err)
	}

	return &jobs.Job{
		ID:              id,
		SubmissionID:    req.SubmissionID,
		TaskID:          req.TaskID,
		Language:        req.Language,
		LanguageVersion: req.LanguageVersion,
		SourceCode:      req.SourceCode,
		CallbackURL:     req.CallbackURL,
		CallbackToken:   req.CallbackToken,
		Limits:          req.Limits,
		Tests:           req.Tests,
		Status:          jobs.StatusQueued,
		CreatedAt:       now,
		UpdatedAt:       now,
	}, nil
}

func (r *JobsRepo) ClaimNextQueued(ctx context.Context) (*jobs.Job, error) {
	row := r.pool.QueryRow(
		ctx,
		`
		WITH picked AS (
			SELECT id
			FROM judge_jobs
			WHERE status = $1 OR (status = $2 AND updated_at < clock_timestamp() - ($3 * interval '1 second'))
			ORDER BY created_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE judge_jobs j
		SET
			status = $2,
			started_at = GREATEST(clock_timestamp(), j.started_at + interval '1 microsecond'),
			updated_at = clock_timestamp(),
			finished_at = NULL
		FROM picked
		WHERE j.id = picked.id
		RETURNING
			j.id,
			j.submission_id,
			j.task_id,
			j.language,
			j.language_version,
			j.source_code,
			j.callback_url,
			j.callback_token,
			j.time_limit_ms,
			j.memory_limit_mb,
			j.output_limit_kb,
			j.tests_json,
			j.status,
			j.created_at,
			j.updated_at,
			j.started_at,
			j.finished_at
		`,
		jobs.StatusQueued,
		jobs.StatusRunning,
		jobs.LeaseDuration.Seconds(),
	)

	var job jobs.Job
	var testsJSON []byte

	err := row.Scan(
		&job.ID,
		&job.SubmissionID,
		&job.TaskID,
		&job.Language,
		&job.LanguageVersion,
		&job.SourceCode,
		&job.CallbackURL,
		&job.CallbackToken,
		&job.Limits.TimeMS,
		&job.Limits.MemoryMB,
		&job.Limits.OutputKB,
		&testsJSON,
		&job.Status,
		&job.CreatedAt,
		&job.UpdatedAt,
		&job.StartedAt,
		&job.FinishedAt,
	)
	if err != nil {
		// no rows = no queued job
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("claim next queued job: %w", err)
	}

	if err := json.Unmarshal(testsJSON, &job.Tests); err != nil {
		return nil, fmt.Errorf("unmarshal tests_json: %w", err)
	}

	return &job, nil
}

func (r *JobsRepo) MarkFinished(ctx context.Context, jobID string, startedAt *time.Time) error {
	if err := r.markTerminal(ctx, jobID, startedAt, jobs.StatusFinished); err != nil {
		return fmt.Errorf("mark job finished: %w", err)
	}
	return nil
}

func (r *JobsRepo) MarkFailed(ctx context.Context, jobID string, startedAt *time.Time) error {
	if err := r.markTerminal(ctx, jobID, startedAt, jobs.StatusFailed); err != nil {
		return fmt.Errorf("mark job failed: %w", err)
	}
	return nil
}

func (r *JobsRepo) markTerminal(ctx context.Context, jobID string, startedAt *time.Time, status jobs.Status) error {
	if startedAt == nil {
		return jobs.ErrLeaseLost
	}
	tag, err := r.pool.Exec(ctx, `UPDATE judge_jobs SET status=$2, finished_at=clock_timestamp(), updated_at=clock_timestamp()
		WHERE id=$1 AND started_at=$3 AND status=$4`, jobID, status, startedAt, jobs.StatusRunning)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return jobs.ErrLeaseLost
	}
	return nil
}

func (r *JobsRepo) RenewLease(ctx context.Context, jobID string, startedAt *time.Time) error {
	if startedAt == nil {
		return jobs.ErrLeaseLost
	}
	tag, err := r.pool.Exec(ctx, `UPDATE judge_jobs SET updated_at=clock_timestamp()
		WHERE id=$1 AND started_at=$2 AND status=$3`, jobID, startedAt, jobs.StatusRunning)
	if err != nil {
		return fmt.Errorf("renew job lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return jobs.ErrLeaseLost
	}
	return nil
}
