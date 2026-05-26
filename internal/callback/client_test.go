package callback

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClientUsesDefaultTimeout(t *testing.T) {
	client := NewClient(0)

	if client.httpClient.Timeout != 10*time.Second {
		t.Fatalf("NewClient(0).Timeout = %s, want 10s", client.httpClient.Timeout)
	}
}

func TestNewClientUsesProvidedTimeout(t *testing.T) {
	client := NewClient(250 * time.Millisecond)

	if client.httpClient.Timeout != 250*time.Millisecond {
		t.Fatalf("NewClient(250ms).Timeout = %s, want 250ms", client.httpClient.Timeout)
	}
}

func TestSendResultPostsPayloadWithJudgeToken(t *testing.T) {
	verdict := "accepted"
	compileTimeMs := 12
	runTimeMs := 34
	peakMemoryKb := 2048
	exitCode := 0
	compileLog := "compiled"
	startedAt := time.Date(2026, 5, 26, 10, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(2 * time.Second)

	var got ResultRequest
	var gotToken string
	var gotContentType string
	var gotMethod string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotToken = r.Header.Get("X-Judge-Token")
		gotContentType = r.Header.Get("Content-Type")

		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode callback request body: %v", err)
		}

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	payload := ResultRequest{
		JudgeSubmissionID: "judge-submission-1",
		SubmissionID:      "submission-1",
		JudgeStatus:       "finished",
		Verdict:           &verdict,
		PassedTests:       1,
		TotalTests:        1,
		CompileTimeMs:     &compileTimeMs,
		RunTimeMs:         &runTimeMs,
		PeakMemoryKb:      &peakMemoryKb,
		Score:             100,
		ExitCode:          &exitCode,
		CompileLog:        &compileLog,
		StartedAt:         &startedAt,
		FinishedAt:        &finishedAt,
		Tests: []TestResult{
			{
				TestCaseID: "case-1",
				Status:     "accepted",
				TimeMs:     &runTimeMs,
				MemoryKb:   &peakMemoryKb,
				ExitCode:   &exitCode,
			},
		},
	}

	client := NewClient(time.Second)
	err := client.SendResult(context.Background(), server.URL, "callback-token", payload)
	if err != nil {
		t.Fatalf("SendResult() error = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("callback method = %q, want POST", gotMethod)
	}
	if gotToken != "callback-token" {
		t.Fatalf("X-Judge-Token = %q, want callback-token", gotToken)
	}
	if gotContentType != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotContentType)
	}
	if got.JudgeSubmissionID != payload.JudgeSubmissionID {
		t.Fatalf("judge_submission_id = %q, want %q", got.JudgeSubmissionID, payload.JudgeSubmissionID)
	}
	if got.Verdict == nil || *got.Verdict != verdict {
		t.Fatalf("verdict = %#v, want %q", got.Verdict, verdict)
	}
	if len(got.Tests) != 1 || got.Tests[0].TestCaseID != "case-1" {
		t.Fatalf("tests = %#v, want one case-1 result", got.Tests)
	}
}

func TestSendResultRejectsEmptyURL(t *testing.T) {
	client := NewClient(time.Second)

	err := client.SendResult(context.Background(), "", "callback-token", ResultRequest{})
	if err == nil {
		t.Fatal("SendResult() error = nil, want error")
	}
	if err.Error() != "callback url is empty" {
		t.Fatalf("SendResult() error = %q, want callback url is empty", err.Error())
	}
}

func TestSendResultRejectsInvalidURL(t *testing.T) {
	client := NewClient(time.Second)

	err := client.SendResult(context.Background(), "://bad-url", "callback-token", ResultRequest{})
	if err == nil {
		t.Fatal("SendResult() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create callback request") {
		t.Fatalf("SendResult() error = %q, want create callback request", err.Error())
	}
}

func TestSendResultReturnsErrorForNonSuccessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client := NewClient(time.Second)
	err := client.SendResult(context.Background(), server.URL, "callback-token", ResultRequest{})
	if err == nil {
		t.Fatal("SendResult() error = nil, want error")
	}
	if err.Error() != "callback failed with status 502" {
		t.Fatalf("SendResult() error = %q, want callback failed with status 502", err.Error())
	}
}

func TestSendResultReturnsErrorWhenContextIsCanceled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := NewClient(time.Second)
	err := client.SendResult(ctx, server.URL, "callback-token", ResultRequest{})
	if err == nil {
		t.Fatal("SendResult() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "send callback request") {
		t.Fatalf("SendResult() error = %q, want send callback request", err.Error())
	}
}
