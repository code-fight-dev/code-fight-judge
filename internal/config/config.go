package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

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

	JudgeWorkDir         string
	JudgeGoImage         string
	JudgePythonImage     string
	JudgeRuntime         string
	JudgeCPUCount        float64
	JudgeDefaultMemoryMB int
	JudgeDefaultPids     int
	JudgeDefaultTmpfsMB  int
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

		JudgeWorkDir:         getString("JUDGE_WORKDIR", "/tmp/code-fight/jobs"),
		JudgeGoImage:         getString("JUDGE_GO_IMAGE", "judge-go:1.24"),
		JudgePythonImage:     getString("JUDGE_PYTHON_IMAGE", "judge-python:3.12"),
		JudgeRuntime:         getString("JUDGE_RUNTIME", "runsc"),
		JudgeCPUCount:        getFloat64("JUDGE_CPU_COUNT", 1),
		JudgeDefaultMemoryMB: getInt("JUDGE_DEFAULT_MEMORY_MB", 256),
		JudgeDefaultPids:     getInt("JUDGE_DEFAULT_PIDS", 64),
		JudgeDefaultTmpfsMB:  getInt("JUDGE_DEFAULT_TMPFS_MB", 64),
	}

	if strings.TrimSpace(cfg.HTTPPort) == "" {
		return Config{}, fmt.Errorf("HTTP_PORT is required")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
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
	if strings.TrimSpace(cfg.JudgeRuntime) == "" {
		return Config{}, fmt.Errorf("JUDGE_RUNTIME is required")
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
