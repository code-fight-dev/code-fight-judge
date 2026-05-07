package http

import (
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"strconv"
	"strings"

	"code-fight-judge/internal/config"
	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/languages"
	"code-fight-judge/internal/storage"
)

type JobsHandler struct {
	repo      *storage.JobsRepo
	languages *languages.Registry
	cfg       config.Config
}

func NewJobsHandler(repo *storage.JobsRepo, languageRegistry *languages.Registry, cfg config.Config) *JobsHandler {
	return &JobsHandler{
		repo:      repo,
		languages: languageRegistry,
		cfg:       cfg,
	}
}

func (h *JobsHandler) CreateSubmission() stdhttp.HandlerFunc {
	return func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		var req jobs.SubmitRequest

		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&req); err != nil {
			writeError(w, stdhttp.StatusBadRequest, "invalid_json", normalizeDecodeError(err))
			return
		}

		if h.cfg.JudgeCallbackURL != "" {
			req.CallbackURL = h.cfg.JudgeCallbackURL
		}
		if h.cfg.JudgeCallbackToken != "" {
			req.CallbackToken = h.cfg.JudgeCallbackToken
		}

		if err := validateSubmitRequest(req, h.languages, h.cfg); err != nil {
			writeError(w, stdhttp.StatusBadRequest, "validation_error", err.Error())
			return
		}

		job, err := h.repo.Create(r.Context(), req)
		if err != nil {
			writeError(w, stdhttp.StatusInternalServerError, "storage_error", err.Error())
			return
		}

		resp := jobs.SubmitResponse{
			JudgeSubmissionID: job.ID,
			Status:            job.Status,
		}

		writeJSON(w, stdhttp.StatusAccepted, resp)
	}
}

func validateSubmitRequest(req jobs.SubmitRequest, languageRegistry *languages.Registry, cfg config.Config) error {
	switch {
	case strings.TrimSpace(req.SubmissionID) == "":
		return errors.New("submission_id is required")
	case strings.TrimSpace(req.TaskID) == "":
		return errors.New("task_id is required")
	case strings.TrimSpace(req.Language) == "":
		return errors.New("language is required")
	case strings.TrimSpace(req.LanguageVersion) == "":
		return errors.New("language_version is required")
	case strings.TrimSpace(req.SourceCode) == "":
		return errors.New("source_code is required")
	case strings.TrimSpace(req.CallbackURL) == "":
		return errors.New("callback_url is required")
	case strings.TrimSpace(req.CallbackToken) == "":
		return errors.New("callback_token is required")
	case req.Limits.TimeMS <= 0:
		return errors.New("limits.time_ms must be > 0")
	case req.Limits.TimeMS > cfg.JudgeMaxTimeMS:
		return errors.New("limits.time_ms exceeds maximum")
	case req.Limits.MemoryMB <= 0:
		return errors.New("limits.memory_mb must be > 0")
	case req.Limits.MemoryMB > cfg.JudgeMaxMemoryMB:
		return errors.New("limits.memory_mb exceeds maximum")
	case req.Limits.OutputKB <= 0:
		return errors.New("limits.output_kb must be > 0")
	case req.Limits.OutputKB > cfg.JudgeMaxOutputKB:
		return errors.New("limits.output_kb exceeds maximum")
	case len(req.Tests) == 0:
		return errors.New("tests must contain at least one test")
	case len(req.Tests) > cfg.JudgeMaxTests:
		return errors.New("tests exceeds maximum")
	}

	if !languageRegistry.IsSupported(req.Language, req.LanguageVersion) {
		return errors.New("language and language_version are not supported")
	}

	for i, test := range req.Tests {
		if strings.TrimSpace(test.ID) == "" {
			return errors.New("tests[" + itoa(i) + "].id must be > 0")
		}
		if test.Points < 0 {
			return errors.New("tests[" + itoa(i) + "].points must be >= 0")
		}
		if len(test.Input)+len(test.ExpectedOutput) > cfg.JudgeMaxTestBytes {
			return errors.New("tests[" + itoa(i) + "] payload exceeds maximum")
		}
	}

	return nil
}

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeError(w stdhttp.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{
		Error:   code,
		Message: message,
	})
}

func normalizeDecodeError(err error) string {
	var syntaxErr *json.SyntaxError
	var unmarshalTypeErr *json.UnmarshalTypeError

	switch {
	case errors.As(err, &syntaxErr):
		return "malformed json"
	case errors.Is(err, stdhttp.ErrBodyNotAllowed):
		return "request body is not allowed"
	case strings.Contains(err.Error(), "http: request body too large"):
		return "request body too large"
	case errors.As(err, &unmarshalTypeErr):
		return "invalid field type for " + unmarshalTypeErr.Field
	default:
		return err.Error()
	}
}

func itoa(v int) string {
	return strconv.Itoa(v)
}
