package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"code-fight-judge/internal/config"
	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/languages"
)

type fakeJobsRepo struct {
	requests []jobs.SubmitRequest
	err      error
}

func (r *fakeJobsRepo) Create(ctx context.Context, req jobs.SubmitRequest) (*jobs.Job, error) {
	r.requests = append(r.requests, req)
	if r.err != nil {
		return nil, r.err
	}

	return &jobs.Job{
		ID:              "judge-job-1",
		SubmissionID:    req.SubmissionID,
		TaskID:          req.TaskID,
		Language:        req.Language,
		LanguageVersion: req.LanguageVersion,
		Status:          jobs.StatusQueued,
	}, nil
}

type fakeReadiness struct {
	checks map[string]string
}

func (r fakeReadiness) CheckReady(ctx context.Context) map[string]string {
	return r.checks
}

type fakeJobMetrics struct {
	createdLanguages []string
}

func (m *fakeJobMetrics) RecordJobCreated(language string) {
	m.createdLanguages = append(m.createdLanguages, language)
}

func TestRouterRequiresJudgeTokenForProtectedEndpoints(t *testing.T) {
	router, _, _ := newTestRouter(t, testConfig(), nil)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodGet, "/languages", nil)

	router.ServeHTTP(response, request)

	if response.Code != stdhttp.StatusUnauthorized {
		t.Fatalf("GET /languages status = %d, want %d", response.Code, stdhttp.StatusUnauthorized)
	}

	var body errorResponse
	decodeJSON(t, response, &body)
	if body.Error != "unauthorized" {
		t.Fatalf("GET /languages error = %q, want unauthorized", body.Error)
	}
}

func TestRouterHealthIsPublic(t *testing.T) {
	router, _, _ := newTestRouter(t, testConfig(), nil)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodGet, "/healthz", nil)

	router.ServeHTTP(response, request)

	if response.Code != stdhttp.StatusOK {
		t.Fatalf("GET /healthz status = %d, want %d", response.Code, stdhttp.StatusOK)
	}

	var body healthResponse
	decodeJSON(t, response, &body)
	if body.Status != "ok" || body.Service != "code-fight-judge" {
		t.Fatalf("GET /healthz response = %#v, want ok code-fight-judge", body)
	}
}

func TestRouterReadinessReturnsDegradedWhenAnyCheckFails(t *testing.T) {
	cfg := testConfig()
	repo := &fakeJobsRepo{}
	registry := languages.NewDefaultRegistry(languages.RuntimeImages{})
	router := NewRouter(
		cfg,
		NewJobsHandler(repo, registry, cfg, nil),
		NewLanguagesHandler(registry),
		fakeReadiness{checks: map[string]string{"db": "ok", "docker": "unavailable"}},
		nil,
	)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodGet, "/readyz", nil)

	router.ServeHTTP(response, request)

	if response.Code != stdhttp.StatusServiceUnavailable {
		t.Fatalf("GET /readyz status = %d, want %d", response.Code, stdhttp.StatusServiceUnavailable)
	}

	var body readinessResponse
	decodeJSON(t, response, &body)
	if body.Status != "degraded" {
		t.Fatalf("GET /readyz status field = %q, want degraded", body.Status)
	}
	if body.Checks["docker"] != "unavailable" {
		t.Fatalf("GET /readyz docker check = %q, want unavailable", body.Checks["docker"])
	}
}

func TestRouterListsSupportedLanguages(t *testing.T) {
	router, _, _ := newTestRouter(t, testConfig(), nil)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodGet, "/languages", nil)
	request.Header.Set("X-Judge-Token", "test-token")

	router.ServeHTTP(response, request)

	if response.Code != stdhttp.StatusOK {
		t.Fatalf("GET /languages status = %d, want %d", response.Code, stdhttp.StatusOK)
	}

	var body []languages.SupportedLanguage
	decodeJSON(t, response, &body)
	want := languages.NewDefaultRegistry(languages.RuntimeImages{}).Supported()
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("GET /languages = %#v, want %#v", body, want)
	}

}

func TestCreateSubmissionAcceptsValidRequest(t *testing.T) {
	router, repo, metrics := newTestRouter(t, testConfig(), nil)

	response := httptest.NewRecorder()
	request := newJSONRequest(t, stdhttp.MethodPost, "/submissions", validSubmitRequest())

	router.ServeHTTP(response, request)

	if response.Code != stdhttp.StatusAccepted {
		t.Fatalf("POST /submissions status = %d, want %d: %s", response.Code, stdhttp.StatusAccepted, response.Body.String())
	}

	var body jobs.SubmitResponse
	decodeJSON(t, response, &body)
	if body.JudgeSubmissionID != "judge-job-1" {
		t.Fatalf("POST /submissions judge_submission_id = %q, want judge-job-1", body.JudgeSubmissionID)
	}
	if body.Status != jobs.StatusQueued {
		t.Fatalf("POST /submissions status = %q, want queued", body.Status)
	}
	if len(repo.requests) != 1 {
		t.Fatalf("repo Create calls = %d, want 1", len(repo.requests))
	}
	if len(metrics.createdLanguages) != 1 || metrics.createdLanguages[0] != "go" {
		t.Fatalf("metrics created languages = %#v, want [go]", metrics.createdLanguages)
	}
}

func TestCreateSubmissionOverridesCallbackFromConfig(t *testing.T) {
	cfg := testConfig()
	cfg.JudgeCallbackURL = "https://backend.example.test/judge/callback"
	cfg.JudgeCallbackToken = "callback-from-config"
	router, repo, _ := newTestRouter(t, cfg, nil)

	requestBody := validSubmitRequest()
	requestBody.CallbackURL = "https://client.example.test/callback"
	requestBody.CallbackToken = "client-token"

	response := httptest.NewRecorder()
	request := newJSONRequest(t, stdhttp.MethodPost, "/submissions", requestBody)

	router.ServeHTTP(response, request)

	if response.Code != stdhttp.StatusAccepted {
		t.Fatalf("POST /submissions status = %d, want %d: %s", response.Code, stdhttp.StatusAccepted, response.Body.String())
	}
	if len(repo.requests) != 1 {
		t.Fatalf("repo Create calls = %d, want 1", len(repo.requests))
	}
	if got, want := repo.requests[0].CallbackURL, cfg.JudgeCallbackURL; got != want {
		t.Fatalf("repo request CallbackURL = %q, want %q", got, want)
	}
	if got, want := repo.requests[0].CallbackToken, cfg.JudgeCallbackToken; got != want {
		t.Fatalf("repo request CallbackToken = %q, want %q", got, want)
	}
}

func TestCreateSubmissionRejectsUnsupportedLanguage(t *testing.T) {
	router, repo, _ := newTestRouter(t, testConfig(), nil)
	requestBody := validSubmitRequest()
	requestBody.Language = "ruby"
	requestBody.LanguageVersion = "3.3"

	response := httptest.NewRecorder()
	request := newJSONRequest(t, stdhttp.MethodPost, "/submissions", requestBody)

	router.ServeHTTP(response, request)

	if response.Code != stdhttp.StatusBadRequest {
		t.Fatalf("POST /submissions status = %d, want %d", response.Code, stdhttp.StatusBadRequest)
	}

	var body errorResponse
	decodeJSON(t, response, &body)
	if body.Error != "validation_error" {
		t.Fatalf("POST /submissions error = %q, want validation_error", body.Error)
	}
	if body.Message != "language and language_version are not supported" {
		t.Fatalf("POST /submissions message = %q, want unsupported language message", body.Message)
	}
	if len(repo.requests) != 0 {
		t.Fatalf("repo Create calls = %d, want 0", len(repo.requests))
	}
}

func TestValidateSubmitRequestRejectsInvalidRequests(t *testing.T) {
	cfg := testConfig()
	registry := languages.NewDefaultRegistry(languages.RuntimeImages{})

	tests := []struct {
		name    string
		mutate  func(*jobs.SubmitRequest)
		wantErr string
	}{
		{
			name: "submission id",
			mutate: func(req *jobs.SubmitRequest) {
				req.SubmissionID = " "
			},
			wantErr: "submission_id is required",
		},
		{
			name: "task id",
			mutate: func(req *jobs.SubmitRequest) {
				req.TaskID = ""
			},
			wantErr: "task_id is required",
		},
		{
			name: "language",
			mutate: func(req *jobs.SubmitRequest) {
				req.Language = ""
			},
			wantErr: "language is required",
		},
		{
			name: "language version",
			mutate: func(req *jobs.SubmitRequest) {
				req.LanguageVersion = ""
			},
			wantErr: "language_version is required",
		},
		{
			name: "source code",
			mutate: func(req *jobs.SubmitRequest) {
				req.SourceCode = ""
			},
			wantErr: "source_code is required",
		},
		{
			name: "callback url",
			mutate: func(req *jobs.SubmitRequest) {
				req.CallbackURL = ""
			},
			wantErr: "callback_url is required",
		},
		{
			name: "callback token",
			mutate: func(req *jobs.SubmitRequest) {
				req.CallbackToken = ""
			},
			wantErr: "callback_token is required",
		},
		{
			name: "time min",
			mutate: func(req *jobs.SubmitRequest) {
				req.Limits.TimeMS = 0
			},
			wantErr: "limits.time_ms must be > 0",
		},
		{
			name: "time max",
			mutate: func(req *jobs.SubmitRequest) {
				req.Limits.TimeMS = cfg.JudgeMaxTimeMS + 1
			},
			wantErr: "limits.time_ms exceeds maximum",
		},
		{
			name: "memory min",
			mutate: func(req *jobs.SubmitRequest) {
				req.Limits.MemoryMB = 0
			},
			wantErr: "limits.memory_mb must be >= 6",
		},
		{
			name: "memory max",
			mutate: func(req *jobs.SubmitRequest) {
				req.Limits.MemoryMB = cfg.JudgeMaxMemoryMB + 1
			},
			wantErr: "limits.memory_mb exceeds maximum",
		},
		{
			name: "output min",
			mutate: func(req *jobs.SubmitRequest) {
				req.Limits.OutputKB = 0
			},
			wantErr: "limits.output_kb must be > 0",
		},
		{
			name: "output max",
			mutate: func(req *jobs.SubmitRequest) {
				req.Limits.OutputKB = cfg.JudgeMaxOutputKB + 1
			},
			wantErr: "limits.output_kb exceeds maximum",
		},
		{
			name: "empty tests",
			mutate: func(req *jobs.SubmitRequest) {
				req.Tests = nil
			},
			wantErr: "tests must contain at least one test",
		},
		{
			name: "too many tests",
			mutate: func(req *jobs.SubmitRequest) {
				req.Tests = make([]jobs.TestCase, cfg.JudgeMaxTests+1)
				for i := range req.Tests {
					req.Tests[i].ID = "case"
				}
			},
			wantErr: "tests exceeds maximum",
		},
		{
			name: "test id",
			mutate: func(req *jobs.SubmitRequest) {
				req.Tests[0].ID = " "
			},
			wantErr: "tests[0].id must be > 0",
		},
		{
			name: "negative points",
			mutate: func(req *jobs.SubmitRequest) {
				req.Tests[0].Points = -1
			},
			wantErr: "tests[0].points must be >= 0",
		},
		{
			name: "payload too large",
			mutate: func(req *jobs.SubmitRequest) {
				req.Tests[0].Input = strings.Repeat("x", cfg.JudgeMaxTestBytes+1)
			},
			wantErr: "tests[0] payload exceeds maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validSubmitRequest()
			tt.mutate(&req)

			err := validateSubmitRequest(req, registry, cfg)
			if err == nil {
				t.Fatalf("validateSubmitRequest() error = nil, want %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("validateSubmitRequest() error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestCreateSubmissionRejectsUnknownJSONField(t *testing.T) {
	router, repo, _ := newTestRouter(t, testConfig(), nil)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodPost, "/submissions", bytes.NewBufferString(`{"submission_id":"sub-1","unknown":true}`))
	request.Header.Set("X-Judge-Token", "test-token")
	request.Header.Set("Content-Type", "application/json")

	router.ServeHTTP(response, request)

	if response.Code != stdhttp.StatusBadRequest {
		t.Fatalf("POST /submissions status = %d, want %d", response.Code, stdhttp.StatusBadRequest)
	}

	var body errorResponse
	decodeJSON(t, response, &body)
	if body.Error != "invalid_json" {
		t.Fatalf("POST /submissions error = %q, want invalid_json", body.Error)
	}
	if len(repo.requests) != 0 {
		t.Fatalf("repo Create calls = %d, want 0", len(repo.requests))
	}
}

func TestNormalizeDecodeError(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "malformed",
			body: `{"submission_id":]`,
			want: "malformed json",
		},
		{
			name: "type",
			body: `{"limits":"bad"}`,
			want: "invalid field type for limits",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req jobs.SubmitRequest
			decoder := json.NewDecoder(strings.NewReader(tt.body))
			decoder.DisallowUnknownFields()

			err := decoder.Decode(&req)
			if err == nil {
				t.Fatal("Decode() error = nil, want error")
			}
			if got := normalizeDecodeError(err); got != tt.want {
				t.Fatalf("normalizeDecodeError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCreateSubmissionReturnsStorageError(t *testing.T) {
	router, _, _ := newTestRouter(t, testConfig(), errors.New("database down"))

	response := httptest.NewRecorder()
	request := newJSONRequest(t, stdhttp.MethodPost, "/submissions", validSubmitRequest())

	router.ServeHTTP(response, request)

	if response.Code != stdhttp.StatusInternalServerError {
		t.Fatalf("POST /submissions status = %d, want %d", response.Code, stdhttp.StatusInternalServerError)
	}

	var body errorResponse
	decodeJSON(t, response, &body)
	if body.Error != "storage_error" {
		t.Fatalf("POST /submissions error = %q, want storage_error", body.Error)
	}
}

func newTestRouter(t *testing.T, cfg config.Config, repoErr error) (stdhttp.Handler, *fakeJobsRepo, *fakeJobMetrics) {
	t.Helper()

	repo := &fakeJobsRepo{err: repoErr}
	registry := languages.NewDefaultRegistry(languages.RuntimeImages{
		Go:     "judge-go:test",
		Python: "judge-python:test",
	})
	metrics := &fakeJobMetrics{}

	jobsHandler := NewJobsHandler(repo, registry, cfg, metrics)
	languagesHandler := NewLanguagesHandler(registry)
	router := NewRouter(cfg, jobsHandler, languagesHandler, fakeReadiness{checks: map[string]string{"db": "ok"}}, nil)

	return router, repo, metrics
}

func testConfig() config.Config {
	return config.Config{
		MaxBodyBytes:       1 << 20,
		JudgeAPIToken:      "test-token",
		JudgeMaxTimeMS:     10000,
		JudgeMaxMemoryMB:   1024,
		JudgeMaxOutputKB:   256,
		JudgeMaxTests:      128,
		JudgeMaxTestBytes:  1 << 20,
		JudgeCallbackToken: "default-callback-token",
		JudgeCallbackURL:   "https://backend.example.test/callback",
	}
}

func validSubmitRequest() jobs.SubmitRequest {
	return jobs.SubmitRequest{
		SubmissionID:    "submission-1",
		TaskID:          "task-1",
		Language:        "go",
		LanguageVersion: "1.24",
		SourceCode:      "package main\n\nfunc Solve(input string) string { return input }\n",
		CallbackURL:     "https://backend.example.test/callback",
		CallbackToken:   "client-callback-token",
		Limits: jobs.Limits{
			TimeMS:   1000,
			MemoryMB: 128,
			OutputKB: 32,
		},
		Tests: []jobs.TestCase{
			{
				ID:             "case-1",
				Input:          "hello",
				ExpectedOutput: "hello",
				Points:         1,
			},
		},
	}
}

func newJSONRequest(t *testing.T, method, target string, body any) *stdhttp.Request {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}

	request := httptest.NewRequest(method, target, bytes.NewReader(payload))
	request.Header.Set("X-Judge-Token", "test-token")
	request.Header.Set("Content-Type", "application/json")

	return request
}

func decodeJSON(t *testing.T, response *httptest.ResponseRecorder, target any) {
	t.Helper()

	if err := json.NewDecoder(response.Body).Decode(target); err != nil {
		t.Fatalf("decode response JSON: %v; body=%q", err, response.Body.String())
	}
}
