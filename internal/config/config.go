package config

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"code-fight-judge/internal/callback"
)

const DefaultCompileTimeout = time.Minute

type Config struct {
	AppEnv          string
	HTTPHost        string
	HTTPPort        string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	MaxBodyBytes    int64
	DatabaseURL     string
	JudgeAPIToken   string

	JudgeWorkDir          string
	JudgeGoImage          string
	JudgePythonImage      string
	JudgeCPPImage         string
	JudgeNodeImage        string
	JudgeRuntime          string
	JudgeCompileTimeout   time.Duration
	JudgeCPUCount         float64
	JudgeDefaultMemoryMB  int
	JudgeDefaultPids      int
	JudgeDefaultTmpfsMB   int
	JudgeMaxTimeMS        int
	JudgeMaxMemoryMB      int
	JudgeMaxOutputKB      int
	JudgeMaxTests         int
	JudgeMaxTestBytes     int
	JudgeCallbackURL      string
	JudgeCallbackToken    string
	JudgeCallbackAttempts int
	JudgeCallbackBackoff  time.Duration
	JudgeMaxOutputDirMB   int
}

func Load() (Config, error) {
	cfg := Config{
		AppEnv:          getString("APP_ENV", "dev"),
		HTTPHost:        getString("HTTP_HOST", "0.0.0.0"),
		HTTPPort:        getString("HTTP_PORT", "8081"),
		ReadTimeout:     getDuration("HTTP_READ_TIMEOUT", 5*time.Second),
		WriteTimeout:    getDuration("HTTP_WRITE_TIMEOUT", 10*time.Second),
		IdleTimeout:     getDuration("HTTP_IDLE_TIMEOUT", 30*time.Second),
		ShutdownTimeout: getDuration("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second),
		MaxBodyBytes:    getInt64("HTTP_MAX_BODY_BYTES", 1<<20),
		DatabaseURL:     strings.TrimSpace(os.Getenv("DATABASE_URL")),
		JudgeAPIToken:   strings.TrimSpace(os.Getenv("JUDGE_API_TOKEN")),

		JudgeWorkDir:          getString("JUDGE_WORKDIR", "/tmp/code-fight/jobs"),
		JudgeGoImage:          getString("JUDGE_GO_IMAGE", "judge-go:1.24"),
		JudgePythonImage:      getString("JUDGE_PYTHON_IMAGE", "judge-python:3.12"),
		JudgeCPPImage:         getString("JUDGE_CPP_IMAGE", "judge-cpp:20"),
		JudgeNodeImage:        getString("JUDGE_NODE_IMAGE", "judge-node:24"),
		JudgeRuntime:          getString("JUDGE_RUNTIME", "runsc"),
		JudgeCompileTimeout:   getDuration("JUDGE_COMPILE_TIMEOUT", DefaultCompileTimeout),
		JudgeCPUCount:         getFloat64("JUDGE_CPU_COUNT", 1),
		JudgeDefaultMemoryMB:  getInt("JUDGE_DEFAULT_MEMORY_MB", 256),
		JudgeDefaultPids:      getInt("JUDGE_DEFAULT_PIDS", 64),
		JudgeDefaultTmpfsMB:   getInt("JUDGE_DEFAULT_TMPFS_MB", 64),
		JudgeMaxTimeMS:        getInt("JUDGE_MAX_TIME_MS", 10000),
		JudgeMaxMemoryMB:      getInt("JUDGE_MAX_MEMORY_MB", 1024),
		JudgeMaxOutputKB:      getInt("JUDGE_MAX_OUTPUT_KB", 256),
		JudgeMaxTests:         getInt("JUDGE_MAX_TESTS", 128),
		JudgeMaxTestBytes:     getInt("JUDGE_MAX_TEST_BYTES", 1<<20),
		JudgeCallbackURL:      strings.TrimSpace(os.Getenv("JUDGE_CALLBACK_URL")),
		JudgeCallbackToken:    strings.TrimSpace(os.Getenv("JUDGE_CALLBACK_TOKEN")),
		JudgeCallbackAttempts: getInt("JUDGE_CALLBACK_ATTEMPTS", 5),
		JudgeCallbackBackoff:  getDuration("JUDGE_CALLBACK_BACKOFF", 500*time.Millisecond),
		JudgeMaxOutputDirMB:   getInt("JUDGE_MAX_OUTPUT_DIR_MB", 128),
	}

	if strings.TrimSpace(cfg.HTTPPort) == "" {
		return Config{}, fmt.Errorf("HTTP_PORT is required")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JudgeAPIToken == "" || cfg.JudgeAPIToken == "dev-judge-api-token" {
		return Config{}, fmt.Errorf("JUDGE_API_TOKEN must be explicitly configured and must not be the development token")
	}
	if cfg.JudgeCallbackURL == "" {
		return Config{}, fmt.Errorf("JUDGE_CALLBACK_URL is required")
	}
	if err := callback.ValidateURL(cfg.JudgeCallbackURL); err != nil {
		return Config{}, fmt.Errorf("JUDGE_CALLBACK_URL: %w", err)
	}
	if cfg.JudgeCallbackToken == "" || cfg.JudgeCallbackToken == "dev-judge-callback-token" {
		return Config{}, fmt.Errorf("JUDGE_CALLBACK_TOKEN must be explicitly configured and must not be the development token")
	}
	if cfg.JudgeCPUCount <= 0 || math.IsNaN(cfg.JudgeCPUCount) || math.IsInf(cfg.JudgeCPUCount, 0) {
		return Config{}, fmt.Errorf("JUDGE_CPU_COUNT must be finite and > 0")
	}
	if cfg.JudgeDefaultPids <= 0 {
		return Config{}, fmt.Errorf("JUDGE_DEFAULT_PIDS must be > 0")
	}
	if cfg.JudgeDefaultTmpfsMB <= 0 {
		return Config{}, fmt.Errorf("JUDGE_DEFAULT_TMPFS_MB must be > 0")
	}
	if cfg.JudgeDefaultMemoryMB < 6 {
		return Config{}, fmt.Errorf("JUDGE_DEFAULT_MEMORY_MB must be >= 6")
	}
	if strings.TrimSpace(cfg.JudgeWorkDir) == "" {
		return Config{}, fmt.Errorf("JUDGE_WORKDIR is required")
	}
	if strings.TrimSpace(cfg.JudgeGoImage) == "" {
		return Config{}, fmt.Errorf("JUDGE_GO_IMAGE is required")
	}
	if strings.TrimSpace(cfg.JudgePythonImage) == "" {
		return Config{}, fmt.Errorf("JUDGE_PYTHON_IMAGE is required")
	}
	if strings.TrimSpace(cfg.JudgeCPPImage) == "" {
		return Config{}, fmt.Errorf("JUDGE_CPP_IMAGE is required")
	}
	if strings.TrimSpace(cfg.JudgeNodeImage) == "" {
		return Config{}, fmt.Errorf("JUDGE_NODE_IMAGE is required")
	}
	if strings.TrimSpace(cfg.JudgeRuntime) == "" {
		return Config{}, fmt.Errorf("JUDGE_RUNTIME is required")
	}
	if cfg.JudgeCompileTimeout <= 0 {
		return Config{}, fmt.Errorf("JUDGE_COMPILE_TIMEOUT must be > 0")
	}
	if cfg.JudgeMaxTimeMS <= 0 {
		return Config{}, fmt.Errorf("JUDGE_MAX_TIME_MS must be > 0")
	}
	if cfg.JudgeMaxMemoryMB < 6 {
		return Config{}, fmt.Errorf("JUDGE_MAX_MEMORY_MB must be >= 6")
	}
	if cfg.JudgeMaxOutputKB <= 0 {
		return Config{}, fmt.Errorf("JUDGE_MAX_OUTPUT_KB must be > 0")
	}
	if cfg.JudgeMaxTests <= 0 {
		return Config{}, fmt.Errorf("JUDGE_MAX_TESTS must be > 0")
	}
	if cfg.JudgeMaxTestBytes <= 0 {
		return Config{}, fmt.Errorf("JUDGE_MAX_TEST_BYTES must be > 0")
	}
	if cfg.JudgeCallbackAttempts <= 0 {
		return Config{}, fmt.Errorf("JUDGE_CALLBACK_ATTEMPTS must be > 0")
	}
	if cfg.JudgeCallbackBackoff <= 0 {
		return Config{}, fmt.Errorf("JUDGE_CALLBACK_BACKOFF must be > 0")
	}
	if cfg.JudgeMaxOutputDirMB <= 0 {
		return Config{}, fmt.Errorf("JUDGE_MAX_OUTPUT_DIR_MB must be > 0")
	}
	if cfg.AppEnv == "prod" {
		if !strings.HasPrefix(cfg.JudgeCallbackURL, "https://") {
			return Config{}, fmt.Errorf("JUDGE_CALLBACK_URL must use HTTPS in prod")
		}
		if cfg.JudgeRuntime != "runsc" {
			return Config{}, fmt.Errorf("JUDGE_RUNTIME must be runsc in prod")
		}
		if !strings.Contains(cfg.JudgeGoImage, "@sha256:") {
			return Config{}, fmt.Errorf("JUDGE_GO_IMAGE must be pinned by digest in prod")
		}
		if !strings.Contains(cfg.JudgePythonImage, "@sha256:") {
			return Config{}, fmt.Errorf("JUDGE_PYTHON_IMAGE must be pinned by digest in prod")
		}
		if !strings.Contains(cfg.JudgeCPPImage, "@sha256:") {
			return Config{}, fmt.Errorf("JUDGE_CPP_IMAGE must be pinned by digest in prod")
		}
		if !strings.Contains(cfg.JudgeNodeImage, "@sha256:") {
			return Config{}, fmt.Errorf("JUDGE_NODE_IMAGE must be pinned by digest in prod")
		}
	}

	return cfg, nil
}

func (c Config) Address() string {
	return c.HTTPHost + ":" + c.HTTPPort
}

func getString(key, fallback string) string {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	return val
}

func getDuration(key string, fallback time.Duration) time.Duration {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}

	d, err := time.ParseDuration(val)
	if err != nil {
		return fallback
	}

	return d
}

func getInt(key string, fallback int) int {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}

	n, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}

	return n
}

func getInt64(key string, fallback int64) int64 {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}

	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return fallback
	}

	return n
}

func getFloat64(key string, fallback float64) float64 {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}

	n, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return fallback
	}

	return n
}
