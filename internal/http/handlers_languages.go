package http

import (
	stdhttp "net/http"

	"code-fight-judge/internal/languages"
)

type LanguagesHandler struct {
	languages *languages.Registry
}

func NewLanguagesHandler(languageRegistry *languages.Registry) *LanguagesHandler {
	return &LanguagesHandler{languages: languageRegistry}
}

func (h *LanguagesHandler) ListSupported() stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		writeJSON(w, stdhttp.StatusOK, h.languages.Supported())
	}
}
