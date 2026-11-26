package storage

import (
	"context"

	"code-fight-judge/internal/jobs"
)

func (r *JobsRepo) QueueSnapshot(ctx context.Context) (depth int64, oldestSeconds float64, err error) {
	err = r.pool.QueryRow(ctx, `SELECT count(*),
		greatest(0, extract(epoch from statement_timestamp()-min(created_at)))::double precision
		FROM judge_jobs
		WHERE result_payload IS NULL AND (
			status=$1 OR (status=$2 AND updated_at < statement_timestamp()-($3 * interval '1 second'))
		)`, jobs.StatusQueued, jobs.StatusRunning, jobs.LeaseDuration.Seconds()).Scan(&depth, &oldestSeconds)
	return
}
