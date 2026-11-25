package storage

import (
	"context"
	"fmt"
	"time"

	"code-fight-judge/internal/jobs"
)

type WorkloadBucket struct {
	Language string `json:"language"`
	Tests    int    `json:"tests"`
	Count    int64  `json:"count"`
}

type WorkloadSnapshot struct {
	Queued   []WorkloadBucket
	Arrivals []WorkloadBucket
	Running  int64
}

type ExecutionSample struct {
	Language       string  `json:"language"`
	Tests          int     `json:"tests"`
	WallSeconds    float64 `json:"wall"`
	CompileSeconds float64 `json:"compile"`
}

func (r *JobsRepo) WorkloadSnapshot(ctx context.Context, window time.Duration) (WorkloadSnapshot, error) {
	var snapshot WorkloadSnapshot
	err := r.pool.QueryRow(ctx, `
		WITH remaining AS (
			SELECT language, jsonb_array_length(tests_json::jsonb) AS tests,
				status = 'running' AND updated_at >= statement_timestamp()-($2 * interval '1 second') AS busy
			FROM judge_jobs
			WHERE status IN ('queued', 'running') AND result_payload IS NULL
		), queued AS (
			SELECT language, tests, count(*) AS count FROM remaining WHERE NOT busy GROUP BY language, tests
		), arrivals AS (
			SELECT language, jsonb_array_length(tests_json::jsonb) AS tests, count(*) AS count
			FROM judge_jobs
			WHERE status IN ('queued', 'running', 'finished', 'failed')
				AND created_at > statement_timestamp()-($1 * interval '1 second')
			GROUP BY language, jsonb_array_length(tests_json::jsonb)
		)
		SELECT (SELECT jsonb_agg(queued) FROM queued), (SELECT jsonb_agg(arrivals) FROM arrivals),
			(SELECT count(*) FROM remaining WHERE busy)
	`, window.Seconds(), jobs.LeaseDuration.Seconds()).Scan(&snapshot.Queued, &snapshot.Arrivals, &snapshot.Running)
	if err != nil {
		return WorkloadSnapshot{}, fmt.Errorf("read Judge workload: %w", err)
	}
	return snapshot, nil
}

func (r *JobsRepo) RecentExecutionSamples(ctx context.Context, window time.Duration) ([]ExecutionSample, error) {
	var samples []ExecutionSample
	err := r.pool.QueryRow(ctx, `
		WITH recent AS (
			SELECT language, tests_json, result_payload, started_at, finished_at
			FROM judge_jobs
			WHERE status = 'finished' AND created_at > statement_timestamp()-($1 * interval '1 second')
			ORDER BY created_at DESC LIMIT 600
		), full_runs AS (
			SELECT language, jsonb_array_length(tests_json::jsonb) AS tests,
				extract(epoch FROM finished_at - started_at)::double precision AS wall,
				coalesce((result_payload->>'compile_time_ms')::double precision / 1000, 0) AS compile
			FROM recent
			WHERE finished_at > started_at AND result_payload->>'verdict' = 'accepted'
		)
		SELECT jsonb_agg(full_runs) FROM full_runs WHERE tests > 0
	`, window.Seconds()).Scan(&samples)
	if err != nil {
		return nil, fmt.Errorf("read Judge execution samples: %w", err)
	}
	return samples, nil
}
