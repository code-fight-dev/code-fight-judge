package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"time"
)

type healthResponse struct {
	Status    string    `json:"status"`
	Service   string    `json:"service"`
	Timestamp time.Time `json:"timestamp"`
}

type ReadinessChecker interface {
	CheckReady(ctx context.Context) map[string]string
}

type readinessResponse struct {
	Status    string            `json:"status"`
	Service   string            `json:"service"`
	Timestamp time.Time         `json:"timestamp"`
	Checks    map[string]string `json:"checks"`
}

func HealthHandler() stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		writeJSON(w, stdhttp.StatusOK, healthResponse{
			Status:    "ok",
			Service:   "code-fight-judge",
			Timestamp: time.Now().UTC(),
		})
	}
}

func ReadyHandler(checker ReadinessChecker) stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		checks := checker.CheckReady(ctx)
		status := "ok"
		code := stdhttp.StatusOK

		for _, check := range checks {
			if check != "ok" {
				status = "degraded"
				code = stdhttp.StatusServiceUnavailable
				break
			}
		}

		writeJSON(w, code, readinessResponse{
			Status:    status,
			Service:   "code-fight-judge",
			Timestamp: time.Now().UTC(),
			Checks:    checks,
		})
	}
}

func writeJSON(w stdhttp.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
