package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	httpClient *http.Client
	targetURL  string
}

func NewClient(timeout time.Duration, targetURL string) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &Client{
		targetURL: targetURL,
		httpClient: &http.Client{
			Timeout: timeout,
			// X-Judge-Token is not one of net/http's protected redirect headers.
			CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

type ResultRequest struct {
	JudgeSubmissionID string       `json:"judge_submission_id"`
	SubmissionID      string       `json:"submission_id"`
	JudgeStatus       string       `json:"judge_status"`
	Verdict           *string      `json:"verdict"`
	ErrorMessage      *string      `json:"error_message"`
	PassedTests       int          `json:"passed_tests"`
	TotalTests        int          `json:"total_tests"`
	CompileTimeMs     *int         `json:"compile_time_ms"`
	RunTimeMs         *int         `json:"run_time_ms"`
	PeakMemoryKb      *int         `json:"peak_memory_kb"`
	Score             int          `json:"score"`
	ExitCode          *int         `json:"exit_code"`
	CompileLog        *string      `json:"compile_log"`
	StdoutTruncated   *string      `json:"stdout_truncated"`
	StderrTruncated   *string      `json:"stderr_truncated"`
	StartedAt         *time.Time   `json:"started_at"`
	FinishedAt        *time.Time   `json:"finished_at"`
	Tests             []TestResult `json:"tests"`
}

type TestResult struct {
	TestCaseID     string  `json:"test_case_id"`
	Status         string  `json:"status"`
	TimeMs         *int    `json:"time_ms"`
	MemoryKb       *int    `json:"memory_kb"`
	ExitCode       *int    `json:"exit_code"`
	CheckerMessage *string `json:"checker_message"`
}

func (c *Client) SendResult(
	ctx context.Context,
	callbackURL string,
	callbackToken string,
	payload ResultRequest,
) error {
	if callbackURL == "" {
		return fmt.Errorf("callback url is empty")
	}
	if callbackURL != c.targetURL {
		return fmt.Errorf("callback url differs from configured target")
	}
	if err := ValidateURL(callbackURL); err != nil {
		return err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal callback payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, callbackURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create callback request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Judge-Token", callbackToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send callback request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("callback failed with status %d", resp.StatusCode)
	}

	return nil
}

// Callback destinations are operator configuration, never submission input.
// Private addresses remain valid for deployments with an internal backend.
func ValidateURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("callback url must be an absolute HTTP(S) URL without credentials or fragment")
	}
	return nil
}
