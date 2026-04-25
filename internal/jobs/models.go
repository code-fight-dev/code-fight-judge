package jobs

import "time"

type Status string

const (
	StatusQueued   Status = "queued"
	StatusRunning  Status = "running"
	StatusFinished Status = "finished"
	StatusFailed   Status = "failed"
)

type Limits struct {
	TimeMS   int `json:"time_ms"`
	MemoryMB int `json:"memory_mb"`
	OutputKB int `json:"output_kb"`
}

type TestCase struct {
	ID             string `json:"id"`
	Input          string `json:"input"`
	ExpectedOutput string `json:"expected_output"`
	Points         int    `json:"points"`
}

type SubmitRequest struct {
	SubmissionID    string     `json:"submission_id"`
	TaskID          string     `json:"task_id"`
	Language        string     `json:"language"`
	LanguageVersion string     `json:"language_version"`
	SourceCode      string     `json:"source_code"`
	CallbackURL     string     `json:"callback_url"`
	CallbackToken   string     `json:"callback_token"`
	Limits          Limits     `json:"limits"`
	Tests           []TestCase `json:"tests"`
}

type SubmitResponse struct {
	JudgeSubmissionID string `json:"judge_submission_id"`
	Status            Status `json:"status"`
}

type Job struct {
	ID              string
	SubmissionID    string
	TaskID          string
	Language        string
	LanguageVersion string
	SourceCode      string
	CallbackURL     string
	CallbackToken   string
	Limits          Limits
	Tests           []TestCase
	Status          Status
	CreatedAt       time.Time
	UpdatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}