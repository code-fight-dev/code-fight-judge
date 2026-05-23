package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type JudgeMetrics struct {
	registry *prometheus.Registry

	httpRequestsTotal   *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec
	httpInFlight        prometheus.Gauge

	jobsCreatedTotal  *prometheus.CounterVec
	jobsRunning       prometheus.Gauge
	jobsFinishedTotal *prometheus.CounterVec
	executionDuration *prometheus.HistogramVec
	callbackAttempts  *prometheus.CounterVec
	sandboxTimeouts   *prometheus.CounterVec
	sandboxOOM        *prometheus.CounterVec
	compileErrors     *prometheus.CounterVec
	runtimeErrors     *prometheus.CounterVec
	maxOutputExceeded *prometheus.CounterVec
}

func NewJudgeMetrics() *JudgeMetrics {
	m := &JudgeMetrics{
		registry: prometheus.NewRegistry(),
		httpRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "codefight_judge_http_requests_total",
				Help: "Total number of judge HTTP requests.",
			},
			[]string{"method", "path", "status"},
		),
		httpRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "codefight_judge_http_request_duration_seconds",
				Help:    "Judge HTTP request duration in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "path"},
		),
		httpInFlight: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "codefight_judge_http_in_flight_requests",
				Help: "Current number of in-flight judge HTTP requests.",
			},
		),
		jobsCreatedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "codefight_judge_jobs_created_total",
				Help: "Total number of judge jobs created.",
			},
			[]string{"language"},
		),
		jobsRunning: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "codefight_judge_jobs_running",
				Help: "Current number of judge jobs running.",
			},
		),
		jobsFinishedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "codefight_judge_jobs_finished_total",
				Help: "Total number of judge jobs finished.",
			},
			[]string{"status", "verdict", "language"},
		),
		executionDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "codefight_judge_execution_duration_seconds",
				Help:    "Judge execution duration in seconds.",
				Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 20, 40, 60},
			},
			[]string{"language"},
		),
		callbackAttempts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "codefight_judge_callback_attempts_total",
				Help: "Total number of judge callback attempts.",
			},
			[]string{"status"},
		),
		sandboxTimeouts: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "codefight_judge_sandbox_timeouts_total",
				Help: "Total number of sandbox timeout verdicts.",
			},
			[]string{"language"},
		),
		sandboxOOM: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "codefight_judge_sandbox_oom_total",
				Help: "Total number of sandbox OOM verdicts.",
			},
			[]string{"language"},
		),
		compileErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "codefight_judge_compile_errors_total",
				Help: "Total number of compile error verdicts.",
			},
			[]string{"language"},
		),
		runtimeErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "codefight_judge_runtime_errors_total",
				Help: "Total number of runtime error verdicts.",
			},
			[]string{"language"},
		),
		maxOutputExceeded: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "codefight_judge_max_output_exceeded_total",
				Help: "Total number of output directory limit verdicts.",
			},
			[]string{"language"},
		),
	}

	m.registry.MustRegister(
		m.httpRequestsTotal,
		m.httpRequestDuration,
		m.httpInFlight,
		m.jobsCreatedTotal,
		m.jobsRunning,
		m.jobsFinishedTotal,
		m.executionDuration,
		m.callbackAttempts,
		m.sandboxTimeouts,
		m.sandboxOOM,
		m.compileErrors,
		m.runtimeErrors,
		m.maxOutputExceeded,
	)

	return m
}

func (m *JudgeMetrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *JudgeMetrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		startedAt := time.Now()
		m.httpInFlight.Inc()

		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		m.httpInFlight.Dec()
		m.httpRequestsTotal.WithLabelValues(
			r.Method,
			routeLabel(r),
			strconv.Itoa(recorder.status),
		).Inc()
		m.httpRequestDuration.WithLabelValues(r.Method, routeLabel(r)).Observe(time.Since(startedAt).Seconds())
	})
}

func (m *JudgeMetrics) RecordJobCreated(language string) {
	m.jobsCreatedTotal.WithLabelValues(language).Inc()
}

func (m *JudgeMetrics) RecordJobStarted() {
	m.jobsRunning.Inc()
}

func (m *JudgeMetrics) RecordJobFinished(status string, verdict string, language string, duration time.Duration) {
	m.jobsRunning.Dec()
	m.jobsFinishedTotal.WithLabelValues(status, verdict, language).Inc()
	m.executionDuration.WithLabelValues(language).Observe(duration.Seconds())

	switch verdict {
	case "time_limit_exceeded":
		m.sandboxTimeouts.WithLabelValues(language).Inc()
	case "memory_limit_exceeded":
		m.sandboxOOM.WithLabelValues(language).Inc()
	case "compile_error":
		m.compileErrors.WithLabelValues(language).Inc()
	case "runtime_error":
		m.runtimeErrors.WithLabelValues(language).Inc()
	case "output_directory_limit_exceeded":
		m.maxOutputExceeded.WithLabelValues(language).Inc()
	}
}

func (m *JudgeMetrics) RecordCallbackAttempt(status string) {
	m.callbackAttempts.WithLabelValues(status).Inc()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func routeLabel(r *http.Request) string {
	if pattern := r.Pattern; pattern != "" {
		return pattern
	}

	return r.URL.Path
}
