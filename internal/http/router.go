package http

import (
	"crypto/subtle"
	stdhttp "net/http"
	"strings"

	"code-fight-judge/internal/config"
	"code-fight-judge/internal/metrics"
)

func NewRouter(cfg config.Config, jobsHandler *JobsHandler, languagesHandler *LanguagesHandler, readiness ReadinessChecker, judgeMetrics *metrics.JudgeMetrics) stdhttp.Handler {
	mux := stdhttp.NewServeMux()

	if judgeMetrics != nil {
		mux.Handle("GET /metrics", judgeMetrics.Handler())
	}
	mux.HandleFunc("GET /healthz", HealthHandler())
	mux.HandleFunc("GET /readyz", ReadyHandler(readiness))
	mux.HandleFunc("GET /languages", requireJudgeToken(cfg.JudgeAPIToken, languagesHandler.ListSupported()))
	mux.HandleFunc("POST /submissions", requireJudgeToken(cfg.JudgeAPIToken, jobsHandler.CreateSubmission()))

	handler := withCommonMiddleware(cfg, mux)
	if judgeMetrics != nil {
		handler = judgeMetrics.Middleware(handler)
	}

	return handler
}

func withCommonMiddleware(cfg config.Config, next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		r.Body = stdhttp.MaxBytesReader(w, r.Body, cfg.MaxBodyBytes)

		next.ServeHTTP(w, r)
	})
}

func requireJudgeToken(expected string, next stdhttp.HandlerFunc) stdhttp.HandlerFunc {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			writeError(w, stdhttp.StatusServiceUnavailable, "misconfigured", "judge authentication is not configured")
		}
	}

	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		received := strings.TrimSpace(r.Header.Get("X-Judge-Token"))
		if received == "" || subtle.ConstantTimeCompare([]byte(expected), []byte(received)) != 1 {
			writeError(w, stdhttp.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}

		next(w, r)
	}
}
