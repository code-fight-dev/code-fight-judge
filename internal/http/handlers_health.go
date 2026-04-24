package http

import (
	"encoding/json"
	stdhttp "net/http"
	"time"
)

type healthResponse struct {
	Status    string    `json:"status"`
	Service   string    `json:"service"`
	Timestamp time.Time `json:"timestamp"`
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

func writeJSON(w stdhttp.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}