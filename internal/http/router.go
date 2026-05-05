package http

import (
	stdhttp "net/http"

	"code-fight-judge/internal/config"
)

func NewRouter(cfg config.Config, jobsHandler *JobsHandler, languagesHandler *LanguagesHandler) stdhttp.Handler {
	mux := stdhttp.NewServeMux()

	mux.HandleFunc("GET /healthz", HealthHandler())
	mux.HandleFunc("GET /languages", languagesHandler.ListSupported())
	mux.HandleFunc("POST /submissions", jobsHandler.CreateSubmission())

	return withCommonMiddleware(cfg, mux)
}

func withCommonMiddleware(cfg config.Config, next stdhttp.Handler) stdhttp.Handler {
	return stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		r.Body = stdhttp.MaxBytesReader(w, r.Body, cfg.MaxBodyBytes)

		next.ServeHTTP(w, r)
	})
}
