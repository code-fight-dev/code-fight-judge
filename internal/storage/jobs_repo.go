package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"code-fight-judge/internal/jobs"

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
	// Backend execution IDs are globally unique UUIDs. Using one as the job
	// key lets concurrent requests and lost HTTP responses reuse one job.
	id := req.SubmissionID

	testsJSON, err := json.Marshal(req.Tests)
	if err != nil {
		return nil, fmt.Errorf("marshal tests: %w", err)
	}

	result, err := r.pool.Exec(
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
		) SELECT
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13, $14, $15, NULL, NULL
		WHERE NOT EXISTS (SELECT 1 FROM judge_jobs WHERE submission_id = $2)
		ON CONFLICT (id) DO NOTHING
		`,
		id,
		req.SubmissionID,
		req.TaskID,
		req.Language,
		req.LanguageVersion,
		req.SourceCode,
		req.CallbackURL,
		"", // Older schemas require a non-null callback_token.
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
	if result.RowsAffected() == 0 {
		var existing jobs.Job
		if err := r.pool.QueryRow(ctx, `SELECT id, status FROM judge_jobs
			WHERE submission_id=$1 ORDER BY created_at, id LIMIT 1`, req.SubmissionID).
			Scan(&existing.ID, &existing.Status); err != nil {
			return nil, fmt.Errorf("find existing judge job: %w", err)
		}
		existing.Replayed = true
		return &existing, nil
	}

	return &jobs.Job{
		ID:              id,
		SubmissionID:    req.SubmissionID,
		TaskID:          req.TaskID,
		Language:        req.Language,
		LanguageVersion: req.LanguageVersion,
		SourceCode:      req.SourceCode,
		CallbackURL:     req.CallbackURL,
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
			SELECT id, started_at IS NULL AS first_attempt
			FROM judge_jobs
			WHERE result_payload IS NULL AND (
				status = $1 OR (status = $2 AND updated_at < clock_timestamp() - ($3 * interval '1 second'))
			)
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
			j.time_limit_ms,
			j.memory_limit_mb,
			j.output_limit_kb,
			j.tests_json,
			j.status,
			j.created_at,
			j.updated_at,
			j.started_at,
			j.finished_at,
			picked.first_attempt
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
		&job.Limits.TimeMS,
		&job.Limits.MemoryMB,
		&job.Limits.OutputKB,
		&testsJSON,
		&job.Status,
		&job.CreatedAt,
		&job.UpdatedAt,
		&job.StartedAt,
		&job.FinishedAt,
		&job.FirstAttempt,
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
