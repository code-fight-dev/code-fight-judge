//go:build integration

package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"code-fight-judge/internal/callback"
	"code-fight-judge/internal/jobs"

	"github.com/jackc/pgx/v5/pgxpool"
)

const judgeJobsSchemaSQL = `
CREATE TABLE IF NOT EXISTS judge_jobs (
    id UUID PRIMARY KEY,
    submission_id UUID NOT NULL,
    task_id UUID NOT NULL,
    language TEXT NOT NULL,
    language_version TEXT NOT NULL,
    source_code TEXT NOT NULL,
    callback_url TEXT NOT NULL,
    callback_token TEXT NOT NULL,
    time_limit_ms INTEGER NOT NULL CHECK (time_limit_ms > 0),
    memory_limit_mb INTEGER NOT NULL CHECK (memory_limit_mb > 0),
    output_limit_kb INTEGER NOT NULL CHECK (output_limit_kb > 0),
    tests_json JSONB NOT NULL,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    started_at TIMESTAMPTZ NULL,
    finished_at TIMESTAMPTZ NULL
);

CREATE INDEX IF NOT EXISTS idx_judge_jobs_status_created_at
    ON judge_jobs (status, created_at);
`

func TestJobsRepoIntegrationPostgresLifecycle(t *testing.T) {
	databaseURL := integrationDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	adminPool, err := NewPostgresPool(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect integration postgres: %v", err)
	}
	defer adminPool.Close()

	schema := "judge_storage_it_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_")
	if _, err := adminPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	defer func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dropCancel()
		_, _ = adminPool.Exec(dropCtx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	}()

	pool := newSchemaPool(t, ctx, databaseURL, schema)
	defer pool.Close()

	if _, err := pool.Exec(ctx, judgeJobsSchemaSQL); err != nil {
		t.Fatalf("create judge_jobs schema: %v", err)
	}

	repo := NewJobsRepo(pool)
	if err := repo.CheckResultSchema(ctx); err == nil {
		t.Fatal("result schema check succeeded before migration")
	}
	if job, err := repo.ClaimNextQueued(ctx); err == nil || job != nil {
		t.Fatalf("execution claim before migration = %#v, %v", job, err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE judge_jobs
		ADD COLUMN result_payload JSON,
		ADD COLUMN callback_next_attempt_at TIMESTAMPTZ,
		ADD COLUMN callback_attempts BIGINT NOT NULL DEFAULT 0`); err != nil {
		t.Fatalf("apply result schema: %v", err)
	}
	if err := repo.CheckResultSchema(ctx); err != nil {
		t.Fatal(err)
	}

	first := submitRequest("11111111-1111-1111-1111-111111111111", "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", "case-1")
	second := submitRequest("22222222-2222-2222-2222-222222222222", "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", "case-2")

	const deliveries = 4
	var wg sync.WaitGroup
	var responses [deliveries]*jobs.Job
	var failures [deliveries]error
	for i := range responses {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses[i], failures[i] = repo.Create(ctx, first)
		}()
	}
	wg.Wait()
	created := 0
	for i, response := range responses {
		if failures[i] != nil || response == nil || response.ID != first.SubmissionID {
			t.Fatalf("delivery %d: job ID does not match the submission, error %v", i, failures[i])
		}
		if !response.Replayed {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d jobs for repeated delivery, want 1", created)
	}
	firstJob := responses[0]
	secondJob, err := repo.Create(ctx, second)
	if err != nil {
		t.Fatalf("Create(second) error = %v", err)
	}
	if firstJob.Status != jobs.StatusQueued || secondJob.Status != jobs.StatusQueued {
		t.Fatalf("created statuses = %q/%q, want queued/queued", firstJob.Status, secondJob.Status)
	}
	if depth, age, err := repo.QueueSnapshot(ctx); err != nil || depth != 2 || age < 0 {
		t.Fatalf("queue snapshot = %d, %f, %v", depth, age, err)
	}

	claimedFirst, err := repo.ClaimNextQueued(ctx)
	if err != nil {
		t.Fatalf("ClaimNextQueued(first) error = %v", err)
	}
	if claimedFirst == nil {
		t.Fatal("ClaimNextQueued(first) = nil")
	}
	if claimedFirst.ID != firstJob.ID {
		t.Fatalf("ClaimNextQueued(first).ID = %q, want %q", claimedFirst.ID, firstJob.ID)
	}
	if claimedFirst.Status != jobs.StatusRunning || !claimedFirst.FirstAttempt {
		t.Fatalf("ClaimNextQueued(first).Status = %q, want running", claimedFirst.Status)
	}
	if claimedFirst.StartedAt == nil {
		t.Fatal("ClaimNextQueued(first).StartedAt = nil")
	}
	if len(claimedFirst.Tests) != 1 || claimedFirst.Tests[0].ID != "case-1" {
		t.Fatalf("ClaimNextQueued(first).Tests = %#v, want case-1", claimedFirst.Tests)
	}

	accepted := resultFor(claimedFirst, "accepted")
	accepted.Tests = []callback.TestResult{{TestCaseID: "case-1", Status: "accepted"}}
	if err := repo.SaveResult(ctx, jobs.StatusFinished, accepted); err != nil {
		t.Fatal(err)
	}
	assertStoredStatus(t, ctx, pool, claimedFirst.ID, jobs.StatusFinished)

	delivery, err := repo.ClaimResult(ctx, time.Minute)
	if err != nil || delivery == nil || delivery.Attempt != 1 || !reflect.DeepEqual(delivery.Payload, accepted) {
		t.Fatalf("claim result = %#v, %v", delivery, err)
	}
	restarted := NewJobsRepo(pool)
	if pending, err := restarted.ClaimResult(ctx, time.Minute); err != nil || pending != nil {
		t.Fatalf("active delivery claim was reclaimed: %#v, %v", pending, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE judge_jobs SET callback_next_attempt_at=now() WHERE id=$1", claimedFirst.ID); err != nil {
		t.Fatal(err)
	}
	replay, err := restarted.ClaimResult(ctx, time.Minute)
	if err != nil || replay == nil || replay.Attempt != 2 || !reflect.DeepEqual(replay.Payload, delivery.Payload) {
		t.Fatalf("replayed result = %#v, %v", replay, err)
	}
	if err := repo.FinishResultDelivery(ctx, claimedFirst.ID, delivery.Attempt, nil); err != nil {
		t.Fatal(err)
	}
	var pending bool
	if err := pool.QueryRow(ctx, "SELECT callback_next_attempt_at IS NOT NULL FROM judge_jobs WHERE id=$1",
		claimedFirst.ID).Scan(&pending); err != nil || !pending {
		t.Fatalf("old delivery changed current claim: pending=%t, %v", pending, err)
	}
	if err := restarted.FinishResultDelivery(ctx, claimedFirst.ID, replay.Attempt, nil); err != nil {
		t.Fatal(err)
	}
	if pending, err := restarted.ClaimResult(ctx, time.Minute); err != nil || pending != nil {
		t.Fatalf("acknowledged result claimed again: %#v, %v", pending, err)
	}

	claimedSecond, err := repo.ClaimNextQueued(ctx)
	if err != nil {
		t.Fatalf("ClaimNextQueued(second) error = %v", err)
	}
	if claimedSecond == nil {
		t.Fatal("ClaimNextQueued(second) = nil")
	}
	if claimedSecond.ID != secondJob.ID {
		t.Fatalf("ClaimNextQueued(second).ID = %q, want %q", claimedSecond.ID, secondJob.ID)
	}
	if err := repo.SaveResult(ctx, jobs.StatusFailed, resultFor(claimedSecond, "system_error")); err != nil {
		t.Fatal(err)
	}
	assertStoredStatus(t, ctx, pool, claimedSecond.ID, jobs.StatusFailed)

	empty, err := repo.ClaimNextQueued(ctx)
	if err != nil {
		t.Fatalf("ClaimNextQueued(empty) error = %v", err)
	}
	if empty != nil {
		t.Fatalf("ClaimNextQueued(empty) = %#v, want nil", empty)
	}

	// A live claim is not stolen; an expired claim is retried and its old owner
	// can neither renew it nor complete the new owner's execution.
	third, err := repo.Create(ctx, submitRequest("33333333-3333-3333-3333-333333333333", "cccccccc-cccc-cccc-cccc-cccccccccccc", "case-3"))
	if err != nil {
		t.Fatal(err)
	}
	oldClaim, err := repo.ClaimNextQueued(ctx)
	if err != nil || oldClaim == nil || oldClaim.ID != third.ID {
		t.Fatalf("claim=%#v err=%v", oldClaim, err)
	}
	if err := repo.RenewLease(ctx, oldClaim.ID, oldClaim.StartedAt); err != nil {
		t.Fatal(err)
	}
	if live, err := repo.ClaimNextQueued(ctx); err != nil || live != nil {
		t.Fatalf("live claim stolen: %#v err=%v", live, err)
	}
	// A previous worker's clock may have been ahead of the database clock.
	if err := pool.QueryRow(ctx, `UPDATE judge_jobs SET updated_at=clock_timestamp()-interval '3 minutes',
		started_at=clock_timestamp()+interval '1 day' WHERE id=$1 RETURNING started_at`, oldClaim.ID).Scan(&oldClaim.StartedAt); err != nil {
		t.Fatal(err)
	}
	newClaim, err := repo.ClaimNextQueued(ctx)
	if err != nil || newClaim == nil || newClaim.ID != oldClaim.ID {
		t.Fatalf("reclaim=%#v err=%v", newClaim, err)
	}
	if newClaim.StartedAt == nil || !newClaim.StartedAt.After(*oldClaim.StartedAt) {
		t.Fatalf("attempt marker did not increase: old=%v new=%v", oldClaim.StartedAt, newClaim.StartedAt)
	}
	if newClaim.FirstAttempt {
		t.Fatal("reclaimed execution counted as a first attempt")
	}
	if err := repo.RenewLease(ctx, oldClaim.ID, oldClaim.StartedAt); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale renewal=%v", err)
	}
	if err := repo.SaveResult(ctx, jobs.StatusFinished, resultFor(oldClaim, "accepted")); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("stale completion=%v", err)
	}
	if err := repo.SaveResult(ctx, jobs.StatusFinished, resultFor(newClaim, "accepted")); err != nil {
		t.Fatal(err)
	}
}

func resultFor(job *jobs.Job, verdict string) callback.ResultRequest {
	startedAt := job.StartedAt.UTC()
	finishedAt := time.Now().UTC()
	return callback.ResultRequest{
		JudgeSubmissionID: job.ID, SubmissionID: job.SubmissionID, JudgeStatus: "finished",
		StartedAt: &startedAt, FinishedAt: &finishedAt, Verdict: &verdict, Tests: []callback.TestResult{},
	}
}

func integrationDatabaseURL(t *testing.T) string {
	t.Helper()

	databaseURL := strings.TrimSpace(os.Getenv("JUDGE_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("set JUDGE_TEST_DATABASE_URL or DATABASE_URL to run Postgres integration tests")
	}

	return databaseURL
}

func newSchemaPool(t *testing.T, ctx context.Context, databaseURL string, schema string) *pgxpool.Pool {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse database url: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 1

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("create schema pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping schema pool: %v", err)
	}

	return pool
}

func submitRequest(submissionID string, taskID string, testID string) jobs.SubmitRequest {
	return jobs.SubmitRequest{
		SubmissionID:    submissionID,
		TaskID:          taskID,
		Language:        "go",
		LanguageVersion: "1.24",
		SourceCode:      "package main\n\nfunc Solve(input string) string { return input }\n",
		CallbackURL:     "https://backend.example.test/internal/judge/callback",
		CallbackToken:   "callback-token",
		Limits: jobs.Limits{
			TimeMS:   1000,
			MemoryMB: 128,
			OutputKB: 64,
		},
		Tests: []jobs.TestCase{
			{
				ID:             testID,
				Input:          "hello",
				ExpectedOutput: "hello",
				Points:         1,
			},
		},
	}
}

func assertStoredStatus(t *testing.T, ctx context.Context, pool *pgxpool.Pool, jobID string, want jobs.Status) {
	t.Helper()

	var status jobs.Status
	var finishedAt *time.Time
	err := pool.QueryRow(ctx, `SELECT status, finished_at FROM judge_jobs WHERE id = $1`, jobID).Scan(&status, &finishedAt)
	if err != nil {
		t.Fatalf("read stored status for %s: %v", jobID, err)
	}
	if status != want {
		t.Fatalf("stored status for %s = %q, want %q", jobID, status, want)
	}
	if finishedAt == nil {
		t.Fatalf("stored finished_at for %s = nil", jobID)
	}

	var queuedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM judge_jobs WHERE status = $1`, jobs.StatusQueued).Scan(&queuedCount); err != nil {
		t.Fatalf("count queued jobs: %v", err)
	}
	if queuedCount < 0 {
		t.Fatalf("queued count cannot be negative: %d", queuedCount)
	}
}

func TestJobsRepoIntegrationUsesIsolatedSchemaNameFormat(t *testing.T) {
	schema := "judge_storage_it_" + strings.ReplaceAll(time.Now().UTC().Format("20060102150405.000000000"), ".", "_")
	if !strings.HasPrefix(schema, "judge_storage_it_") {
		t.Fatalf("schema = %q, want judge_storage_it_ prefix", schema)
	}
	if strings.Contains(schema, ".") {
		t.Fatalf("schema = %q, should not contain dot", schema)
	}
	if strings.Contains(schema, "-") {
		t.Fatalf("schema = %q, should not contain dash", schema)
	}
	if len(schema) > 63 {
		t.Fatalf("schema = %q length %d exceeds postgres identifier limit", schema, len(schema))
	}
}

func Example_integrationCommand() {
	fmt.Println("go test ./internal/storage -tags=integration")
	// Output: go test ./internal/storage -tags=integration
}
