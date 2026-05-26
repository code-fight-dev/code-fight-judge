package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJudgeMetricsRecordsJobsAndCallbacks(t *testing.T) {
	judgeMetrics := NewJudgeMetrics()

	judgeMetrics.RecordJobCreated("go")
	judgeMetrics.RecordJobStarted()
	judgeMetrics.RecordJobFinished("finished", "time_limit_exceeded", "go", 250*time.Millisecond)
	judgeMetrics.RecordJobStarted()
	judgeMetrics.RecordJobFinished("failed", "memory_limit_exceeded", "python", 500*time.Millisecond)
	judgeMetrics.RecordJobStarted()
	judgeMetrics.RecordJobFinished("failed", "compile_error", "go", time.Second)
	judgeMetrics.RecordJobStarted()
	judgeMetrics.RecordJobFinished("failed", "runtime_error", "go", time.Second)
	judgeMetrics.RecordJobStarted()
	judgeMetrics.RecordJobFinished("failed", "output_directory_limit_exceeded", "go", time.Second)
	judgeMetrics.RecordCallbackAttempt("success")
	judgeMetrics.RecordCallbackAttempt("error")

	body := scrapeMetrics(t, judgeMetrics)

	for _, want := range []string{
		`codefight_judge_jobs_created_total{language="go"} 1`,
		`codefight_judge_jobs_running 0`,
		`codefight_judge_jobs_finished_total{language="go",status="finished",verdict="time_limit_exceeded"} 1`,
		`codefight_judge_jobs_finished_total{language="python",status="failed",verdict="memory_limit_exceeded"} 1`,
		`codefight_judge_sandbox_timeouts_total{language="go"} 1`,
		`codefight_judge_sandbox_oom_total{language="python"} 1`,
		`codefight_judge_compile_errors_total{language="go"} 1`,
		`codefight_judge_runtime_errors_total{language="go"} 1`,
		`codefight_judge_max_output_exceeded_total{language="go"} 1`,
		`codefight_judge_callback_attempts_total{status="success"} 1`,
		`codefight_judge_callback_attempts_total{status="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body does not contain %q\n%s", want, body)
		}
	}
}

func TestJudgeMetricsMiddlewareRecordsHTTPStatusAndRoute(t *testing.T) {
	judgeMetrics := NewJudgeMetrics()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	handler := judgeMetrics.Middleware(next)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/submissions", nil)
	request.Pattern = "POST /submissions"

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("middleware response code = %d, want %d", response.Code, http.StatusAccepted)
	}

	body := scrapeMetrics(t, judgeMetrics)
	for _, want := range []string{
		`codefight_judge_http_requests_total{method="POST",path="POST /submissions",status="202"} 1`,
		`codefight_judge_http_request_duration_seconds_count{method="POST",path="POST /submissions"} 1`,
		`codefight_judge_http_in_flight_requests 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body does not contain %q\n%s", want, body)
		}
	}
}

func TestJudgeMetricsMiddlewareUsesDefaultStatusAndURLPath(t *testing.T) {
	judgeMetrics := NewJudgeMetrics()
	handler := judgeMetrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("middleware response code = %d, want %d", response.Code, http.StatusOK)
	}

	body := scrapeMetrics(t, judgeMetrics)
	if !strings.Contains(body, `codefight_judge_http_requests_total{method="GET",path="/healthz",status="200"} 1`) {
		t.Fatalf("metrics body does not contain /healthz 200 request\n%s", body)
	}
}

func TestJudgeMetricsMiddlewareSkipsMetricsEndpoint(t *testing.T) {
	judgeMetrics := NewJudgeMetrics()
	handler := judgeMetrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("middleware response code = %d, want %d", response.Code, http.StatusNoContent)
	}

	body := scrapeMetrics(t, judgeMetrics)
	if strings.Contains(body, `codefight_judge_http_requests_total`) {
		t.Fatalf("metrics endpoint should not be recorded\n%s", body)
	}
}

func TestStatusRecorderWriteHeader(t *testing.T) {
	response := httptest.NewRecorder()
	recorder := &statusRecorder{ResponseWriter: response, status: http.StatusOK}

	recorder.WriteHeader(http.StatusTeapot)

	if recorder.status != http.StatusTeapot {
		t.Fatalf("statusRecorder.status = %d, want %d", recorder.status, http.StatusTeapot)
	}
	if response.Code != http.StatusTeapot {
		t.Fatalf("response code = %d, want %d", response.Code, http.StatusTeapot)
	}
}

func scrapeMetrics(t *testing.T, judgeMetrics *JudgeMetrics) string {
	t.Helper()

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	judgeMetrics.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("metrics scrape status = %d, want %d", response.Code, http.StatusOK)
	}

	return response.Body.String()
}
