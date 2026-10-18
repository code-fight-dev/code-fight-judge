package storage

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"code-fight-judge/internal/jobs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type execCall struct {
	sql  string
	args []any
}

type fakeJobsDB struct {
	execErr error
	execs   []execCall
	row     pgx.Row
	queries []execCall
}

func (db *fakeJobsDB) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	db.execs = append(db.execs, execCall{
		sql:  sql,
		args: append([]any(nil), arguments...),
	})
	return pgconn.NewCommandTag("UPDATE 1"), db.execErr
}

func (db *fakeJobsDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	db.queries = append(db.queries, execCall{
		sql:  sql,
		args: append([]any(nil), args...),
	})
	if db.row == nil {
		return fakeRow{err: pgx.ErrNoRows}
	}

	return db.row
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return errors.New("scan destination count mismatch")
	}

	for i, value := range r.values {
		switch target := dest[i].(type) {
		case *string:
			*target = value.(string)
		case *int:
			*target = value.(int)
		case *[]byte:
			*target = value.([]byte)
		case *jobs.Status:
			*target = value.(jobs.Status)
		case *time.Time:
			*target = value.(time.Time)
		case **time.Time:
			switch v := value.(type) {
			case *time.Time:
				*target = v
			case nil:
				*target = nil
			default:
				return errors.New("unsupported nullable time value")
			}
		default:
			return errors.New("unsupported scan target")
		}
	}

	return nil
}

func TestJobsRepoCreateInsertsQueuedJob(t *testing.T) {
	db := &fakeJobsDB{}
	repo := &JobsRepo{pool: db}
	req := jobs.SubmitRequest{
		SubmissionID:    "submission-1",
		TaskID:          "task-1",
		Language:        "go",
		LanguageVersion: "1.24",
		SourceCode:      "package main",
		CallbackURL:     "https://backend.example.test/callback",
		CallbackToken:   "callback-token",
		Limits: jobs.Limits{
			TimeMS:   1000,
			MemoryMB: 128,
			OutputKB: 64,
		},
		Tests: []jobs.TestCase{
			{
				ID:             "case-1",
				Input:          "in",
				ExpectedOutput: "out",
				Points:         5,
			},
		},
	}

	job, err := repo.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if job.ID == "" {
		t.Fatal("Create().ID is empty")
	}
	if job.Status != jobs.StatusQueued {
		t.Fatalf("Create().Status = %q, want queued", job.Status)
	}
	if job.SubmissionID != req.SubmissionID || job.TaskID != req.TaskID {
		t.Fatalf("Create() job ids = %q/%q, want request ids", job.SubmissionID, job.TaskID)
	}
	if len(db.execs) != 1 {
		t.Fatalf("Exec calls = %d, want 1", len(db.execs))
	}

	call := db.execs[0]
	if !strings.Contains(call.sql, "INSERT INTO judge_jobs") {
		t.Fatalf("Exec SQL = %q, want INSERT INTO judge_jobs", call.sql)
	}
	if len(call.args) != 15 {
		t.Fatalf("Exec args len = %d, want 15", len(call.args))
	}
	if call.args[1] != req.SubmissionID || call.args[2] != req.TaskID {
		t.Fatalf("Exec request ids = %#v/%#v, want request ids", call.args[1], call.args[2])
	}
	if call.args[12] != jobs.StatusQueued {
		t.Fatalf("Exec status arg = %#v, want queued", call.args[12])
	}

	var storedTests []jobs.TestCase
	if err := json.Unmarshal([]byte(call.args[11].(string)), &storedTests); err != nil {
		t.Fatalf("unmarshal tests arg: %v", err)
	}
	if len(storedTests) != 1 || storedTests[0].ID != "case-1" {
		t.Fatalf("stored tests = %#v, want case-1", storedTests)
	}
}

func TestNewJobsRepo(t *testing.T) {
	repo := NewJobsRepo(nil)

	if repo == nil {
		t.Fatal("NewJobsRepo() = nil")
	}
}

func TestJobsRepoCreateWrapsInsertError(t *testing.T) {
	db := &fakeJobsDB{execErr: errors.New("insert failed")}
	repo := &JobsRepo{pool: db}

	_, err := repo.Create(context.Background(), jobs.SubmitRequest{})
	if err == nil {
		t.Fatal("Create() error = nil, want insert error")
	}
	if !strings.Contains(err.Error(), "insert judge job") {
		t.Fatalf("Create() error = %q, want insert judge job", err.Error())
	}
}

func TestJobsRepoClaimNextQueuedReturnsJob(t *testing.T) {
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	startedAt := now.Add(time.Second)
	testsJSON, err := json.Marshal([]jobs.TestCase{
		{
			ID:             "case-1",
			Input:          "in",
			ExpectedOutput: "out",
			Points:         10,
		},
	})
	if err != nil {
		t.Fatalf("marshal tests: %v", err)
	}
	db := &fakeJobsDB{row: fakeRow{values: []any{
		"job-1",
		"submission-1",
		"task-1",
		"go",
		"1.24",
		"package main",
		"https://backend.example.test/callback",
		"callback-token",
		1000,
		128,
		64,
		testsJSON,
		jobs.StatusRunning,
		now,
		now,
		&startedAt,
		nil,
	}}}
	repo := &JobsRepo{pool: db}

	job, err := repo.ClaimNextQueued(context.Background())
	if err != nil {
		t.Fatalf("ClaimNextQueued() error = %v", err)
	}

	if job == nil {
		t.Fatal("ClaimNextQueued() job = nil")
	}
	if job.ID != "job-1" || job.Status != jobs.StatusRunning {
		t.Fatalf("ClaimNextQueued() job = %#v, want job-1 running", job)
	}
	if job.StartedAt == nil || !job.StartedAt.Equal(startedAt) {
		t.Fatalf("ClaimNextQueued() StartedAt = %#v, want %s", job.StartedAt, startedAt)
	}
	if len(job.Tests) != 1 || job.Tests[0].ID != "case-1" {
		t.Fatalf("ClaimNextQueued() tests = %#v, want case-1", job.Tests)
	}
	if len(db.queries) != 1 {
		t.Fatalf("QueryRow calls = %d, want 1", len(db.queries))
	}
	if !strings.Contains(db.queries[0].sql, "FOR UPDATE SKIP LOCKED") {
		t.Fatalf("QueryRow SQL = %q, want SKIP LOCKED", db.queries[0].sql)
	}
	if db.queries[0].args[0] != jobs.StatusQueued || db.queries[0].args[1] != jobs.StatusRunning {
		t.Fatalf("QueryRow status args = %#v, want queued/running", db.queries[0].args[:2])
	}
}

func TestJobsRepoClaimNextQueuedReturnsNilForNoRows(t *testing.T) {
	db := &fakeJobsDB{row: fakeRow{err: pgx.ErrNoRows}}
	repo := &JobsRepo{pool: db}

	job, err := repo.ClaimNextQueued(context.Background())
	if err != nil {
		t.Fatalf("ClaimNextQueued() error = %v", err)
	}
	if job != nil {
		t.Fatalf("ClaimNextQueued() job = %#v, want nil", job)
	}
}

func TestJobsRepoClaimNextQueuedWrapsScanError(t *testing.T) {
	db := &fakeJobsDB{row: fakeRow{err: errors.New("scan failed")}}
	repo := &JobsRepo{pool: db}

	_, err := repo.ClaimNextQueued(context.Background())
	if err == nil {
		t.Fatal("ClaimNextQueued() error = nil, want scan error")
	}
	if !strings.Contains(err.Error(), "claim next queued job") {
		t.Fatalf("ClaimNextQueued() error = %q, want claim next queued job", err.Error())
	}
}

func TestJobsRepoClaimNextQueuedWrapsInvalidTestsJSON(t *testing.T) {
	now := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	db := &fakeJobsDB{row: fakeRow{values: []any{
		"job-1",
		"submission-1",
		"task-1",
		"go",
		"1.24",
		"package main",
		"https://backend.example.test/callback",
		"callback-token",
		1000,
		128,
		64,
		[]byte(`{not-json`),
		jobs.StatusRunning,
		now,
		now,
		nil,
		nil,
	}}}
	repo := &JobsRepo{pool: db}

	_, err := repo.ClaimNextQueued(context.Background())
	if err == nil {
		t.Fatal("ClaimNextQueued() error = nil, want unmarshal error")
	}
	if !strings.Contains(err.Error(), "unmarshal tests_json") {
		t.Fatalf("ClaimNextQueued() error = %q, want unmarshal tests_json", err.Error())
	}
}

func TestJobsRepoMarkFinishedAndFailed(t *testing.T) {
	tests := []struct {
		name       string
		mark       func(*JobsRepo, context.Context, string, *time.Time) error
		wantStatus jobs.Status
		wantSQL    string
	}{
		{
			name:       "finished",
			mark:       (*JobsRepo).MarkFinished,
			wantStatus: jobs.StatusFinished,
			wantSQL:    "UPDATE judge_jobs",
		},
		{
			name:       "failed",
			mark:       (*JobsRepo).MarkFailed,
			wantStatus: jobs.StatusFailed,
			wantSQL:    "UPDATE judge_jobs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeJobsDB{}
			repo := &JobsRepo{pool: db}

			err := tt.mark(repo, context.Background(), "job-1", timePtr(time.Now()))
			if err != nil {
				t.Fatalf("mark() error = %v", err)
			}
			if len(db.execs) != 1 {
				t.Fatalf("Exec calls = %d, want 1", len(db.execs))
			}
			if !strings.Contains(db.execs[0].sql, tt.wantSQL) {
				t.Fatalf("Exec SQL = %q, want %q", db.execs[0].sql, tt.wantSQL)
			}
			if db.execs[0].args[0] != "job-1" || db.execs[0].args[1] != tt.wantStatus {
				t.Fatalf("Exec args = %#v, want job-1/%s", db.execs[0].args[:2], tt.wantStatus)
			}
		})
	}
}

func TestJobsRepoMarkErrors(t *testing.T) {
	tests := []struct {
		name    string
		mark    func(*JobsRepo, context.Context, string, *time.Time) error
		wantErr string
	}{
		{
			name:    "finished",
			mark:    (*JobsRepo).MarkFinished,
			wantErr: "mark job finished",
		},
		{
			name:    "failed",
			mark:    (*JobsRepo).MarkFailed,
			wantErr: "mark job failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeJobsDB{execErr: errors.New("update failed")}
			repo := &JobsRepo{pool: db}

			err := tt.mark(repo, context.Background(), "job-1", timePtr(time.Now()))
			if err == nil {
				t.Fatalf("mark() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("mark() error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func timePtr(v time.Time) *time.Time { return &v }
