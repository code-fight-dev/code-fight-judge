package http

import (
	"crypto/subtle"
	stdhttp "net/http"
	"strings"

	"code-fight-judge/internal/config"
)

func NewRouter(cfg config.Config, jobsHandler *JobsHandler, languagesHandler *LanguagesHandler, readiness ReadinessChecker) stdhttp.Handler {
	mux := stdhttp.NewServeMux()

	mux.HandleFunc("GET /healthz", HealthHandler())
	mux.HandleFunc("GET /readyz", ReadyHandler(readiness))
	mux.HandleFunc("GET /languages", requireJudgeToken(cfg.JudgeAPIToken, languagesHandler.ListSupported()))
	mux.HandleFunc("POST /submissions", requireJudgeToken(cfg.JudgeAPIToken, jobsHandler.CreateSubmission()))

	return withCommonMiddleware(cfg, mux)
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
		return next
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
