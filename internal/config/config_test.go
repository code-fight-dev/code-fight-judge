package config

import (
	"strings"
	"testing"
	"time"
)

var configEnvKeys = []string{
	"APP_ENV",
	"HTTP_HOST",
	"HTTP_PORT",
	"HTTP_READ_TIMEOUT",
	"HTTP_WRITE_TIMEOUT",
	"HTTP_IDLE_TIMEOUT",
	"HTTP_SHUTDOWN_TIMEOUT",
	"HTTP_MAX_BODY_BYTES",
	"DATABASE_URL",
	"JUDGE_API_TOKEN",
	"JUDGE_WORKDIR",
	"JUDGE_GO_IMAGE",
	"JUDGE_PYTHON_IMAGE",
	"JUDGE_RUNTIME",
	"JUDGE_CPU_COUNT",
	"JUDGE_DEFAULT_MEMORY_MB",
	"JUDGE_DEFAULT_PIDS",
	"JUDGE_DEFAULT_TMPFS_MB",
	"JUDGE_MAX_TIME_MS",
	"JUDGE_MAX_MEMORY_MB",
	"JUDGE_MAX_OUTPUT_KB",
	"JUDGE_MAX_TESTS",
	"JUDGE_MAX_TEST_BYTES",
	"JUDGE_CALLBACK_URL",
	"JUDGE_CALLBACK_TOKEN",
	"JUDGE_CALLBACK_ATTEMPTS",
	"JUDGE_CALLBACK_BACKOFF",
	"JUDGE_MAX_OUTPUT_DIR_MB",
}

func TestLoadUsesDefaultsWithExplicitCredentials(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("DATABASE_URL", "postgres://judge:test@localhost:5432/judge")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.AppEnv != "dev" {
		t.Fatalf("AppEnv = %q, want dev", cfg.AppEnv)
	}
	if cfg.HTTPHost != "0.0.0.0" || cfg.HTTPPort != "8081" {
		t.Fatalf("HTTP address = %q:%q, want 0.0.0.0:8081", cfg.HTTPHost, cfg.HTTPPort)
	}
	if cfg.JudgeAPIToken != "test-api-token" {
		t.Fatalf("JudgeAPIToken = %q, want dev token", cfg.JudgeAPIToken)
	}
	if cfg.JudgeCallbackToken != "test-callback-token" {
		t.Fatalf("JudgeCallbackToken = %q, want dev callback token", cfg.JudgeCallbackToken)
	}
	if cfg.JudgeGoImage != "judge-go:1.24" || cfg.JudgePythonImage != "judge-python:3.12" {
		t.Fatalf("judge images = %q/%q, want defaults", cfg.JudgeGoImage, cfg.JudgePythonImage)
	}
	if cfg.JudgeMaxTimeMS != 10000 || cfg.JudgeMaxMemoryMB != 1024 || cfg.JudgeMaxOutputKB != 256 {
		t.Fatalf("judge limits = time %d memory %d output %d, want defaults", cfg.JudgeMaxTimeMS, cfg.JudgeMaxMemoryMB, cfg.JudgeMaxOutputKB)
	}
}

func TestLoadReadsConfiguredValues(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_ENV", "stage")
	t.Setenv("HTTP_HOST", "127.0.0.1")
	t.Setenv("HTTP_PORT", "9090")
	t.Setenv("HTTP_READ_TIMEOUT", "2s")
	t.Setenv("HTTP_WRITE_TIMEOUT", "3s")
	t.Setenv("HTTP_IDLE_TIMEOUT", "4s")
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "5s")
	t.Setenv("HTTP_MAX_BODY_BYTES", "2048")
	t.Setenv("DATABASE_URL", "postgres://judge:test@localhost:5432/judge")
	t.Setenv("JUDGE_API_TOKEN", "judge-api-token")
	t.Setenv("JUDGE_WORKDIR", "/workdir")
	t.Setenv("JUDGE_GO_IMAGE", "go-image:test")
	t.Setenv("JUDGE_PYTHON_IMAGE", "python-image:test")
	t.Setenv("JUDGE_RUNTIME", "runc")
	t.Setenv("JUDGE_CPU_COUNT", "1.5")
	t.Setenv("JUDGE_DEFAULT_MEMORY_MB", "512")
	t.Setenv("JUDGE_DEFAULT_PIDS", "128")
	t.Setenv("JUDGE_DEFAULT_TMPFS_MB", "32")
	t.Setenv("JUDGE_MAX_TIME_MS", "20000")
	t.Setenv("JUDGE_MAX_MEMORY_MB", "2048")
	t.Setenv("JUDGE_MAX_OUTPUT_KB", "512")
	t.Setenv("JUDGE_MAX_TESTS", "256")
	t.Setenv("JUDGE_MAX_TEST_BYTES", "4096")
	t.Setenv("JUDGE_CALLBACK_URL", "https://backend.example.test/callback")
	t.Setenv("JUDGE_CALLBACK_TOKEN", "callback-token")
	t.Setenv("JUDGE_CALLBACK_ATTEMPTS", "7")
	t.Setenv("JUDGE_CALLBACK_BACKOFF", "750ms")
	t.Setenv("JUDGE_MAX_OUTPUT_DIR_MB", "256")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.AppEnv != "stage" {
		t.Fatalf("AppEnv = %q, want stage", cfg.AppEnv)
	}
	if cfg.Address() != "127.0.0.1:9090" {
		t.Fatalf("Address() = %q, want 127.0.0.1:9090", cfg.Address())
	}
	if cfg.ReadTimeout != 2*time.Second || cfg.WriteTimeout != 3*time.Second || cfg.IdleTimeout != 4*time.Second {
		t.Fatalf("timeouts = %s/%s/%s, want 2s/3s/4s", cfg.ReadTimeout, cfg.WriteTimeout, cfg.IdleTimeout)
	}
	if cfg.ShutdownTimeout != 5*time.Second || cfg.MaxBodyBytes != 2048 {
		t.Fatalf("shutdown/max body = %s/%d, want 5s/2048", cfg.ShutdownTimeout, cfg.MaxBodyBytes)
	}
	if cfg.JudgeCPUCount != 1.5 {
		t.Fatalf("JudgeCPUCount = %v, want 1.5", cfg.JudgeCPUCount)
	}
	if cfg.JudgeCallbackAttempts != 7 || cfg.JudgeCallbackBackoff != 750*time.Millisecond {
		t.Fatalf("callback retry = %d/%s, want 7/750ms", cfg.JudgeCallbackAttempts, cfg.JudgeCallbackBackoff)
	}
	if cfg.JudgeMaxOutputDirMB != 256 {
		t.Fatalf("JudgeMaxOutputDirMB = %d, want 256", cfg.JudgeMaxOutputDirMB)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	clearConfigEnv(t)

	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want DATABASE_URL error")
	}
	if err.Error() != "DATABASE_URL is required" {
		t.Fatalf("Load() error = %q, want DATABASE_URL is required", err.Error())
	}
}

func TestLoadValidatesPositiveLimits(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{
			name:    "max time",
			key:     "JUDGE_MAX_TIME_MS",
			value:   "0",
			wantErr: "JUDGE_MAX_TIME_MS must be > 0",
		},
		{
			name:    "max tests",
			key:     "JUDGE_MAX_TESTS",
			value:   "-1",
			wantErr: "JUDGE_MAX_TESTS must be > 0",
		},
		{
			name:    "callback attempts",
			key:     "JUDGE_CALLBACK_ATTEMPTS",
			value:   "0",
			wantErr: "JUDGE_CALLBACK_ATTEMPTS must be > 0",
		},
		{
			name:    "callback backoff",
			key:     "JUDGE_CALLBACK_BACKOFF",
			value:   "-1s",
			wantErr: "JUDGE_CALLBACK_BACKOFF must be > 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("DATABASE_URL", "postgres://judge:test@localhost:5432/judge")
			t.Setenv(tt.key, tt.value)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("Load() error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoadValidatesProductionRequirements(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{
			name:    "api token",
			env:     map[string]string{},
			wantErr: "JUDGE_API_TOKEN must be explicitly configured and must not be the development token",
		},
		{
			name: "callback url",
			env: map[string]string{
				"JUDGE_API_TOKEN": "judge-api-token",
			},
			wantErr: "JUDGE_CALLBACK_URL is required",
		},
		{
			name: "callback token",
			env: map[string]string{
				"JUDGE_API_TOKEN":    "judge-api-token",
				"JUDGE_CALLBACK_URL": "https://backend.example.test/callback",
			},
			wantErr: "JUDGE_CALLBACK_TOKEN must be explicitly configured and must not be the development token",
		},
		{
			name: "go image digest",
			env: map[string]string{
				"JUDGE_API_TOKEN":      "judge-api-token",
				"JUDGE_CALLBACK_URL":   "https://backend.example.test/callback",
				"JUDGE_CALLBACK_TOKEN": "callback-token",
			},
			wantErr: "JUDGE_GO_IMAGE must be pinned by digest in prod",
		},
		{
			name: "python image digest",
			env: map[string]string{
				"JUDGE_API_TOKEN":      "judge-api-token",
				"JUDGE_CALLBACK_URL":   "https://backend.example.test/callback",
				"JUDGE_CALLBACK_TOKEN": "callback-token",
				"JUDGE_GO_IMAGE":       "judge-go@sha256:abc",
			},
			wantErr: "JUDGE_PYTHON_IMAGE must be pinned by digest in prod",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("APP_ENV", "prod")
			t.Setenv("DATABASE_URL", "postgres://judge:test@localhost:5432/judge")
			t.Setenv("JUDGE_API_TOKEN", "")
			t.Setenv("JUDGE_CALLBACK_URL", "")
			t.Setenv("JUDGE_CALLBACK_TOKEN", "")
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() error = nil, want %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("Load() error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestLoadAcceptsProductionPinnedImages(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("APP_ENV", "prod")
	t.Setenv("DATABASE_URL", "postgres://judge:test@localhost:5432/judge")
	t.Setenv("JUDGE_API_TOKEN", "judge-api-token")
	t.Setenv("JUDGE_CALLBACK_URL", "https://backend.example.test/callback")
	t.Setenv("JUDGE_CALLBACK_TOKEN", "callback-token")
	t.Setenv("JUDGE_GO_IMAGE", "judge-go@sha256:abc")
	t.Setenv("JUDGE_PYTHON_IMAGE", "judge-python@sha256:def")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.JudgeAPIToken != "judge-api-token" {
		t.Fatalf("JudgeAPIToken = %q, want judge-api-token", cfg.JudgeAPIToken)
	}
}

func TestInvalidPrimitiveValuesUseFallbacks(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("STRING_VALUE", " configured ")
	t.Setenv("BAD_DURATION", "not-duration")
	t.Setenv("BAD_INT", "not-int")
	t.Setenv("BAD_INT64", "not-int64")
	t.Setenv("BAD_FLOAT64", "not-float64")

	if got := getString("STRING_VALUE", "fallback"); got != "configured" {
		t.Fatalf("getString() = %q, want configured", got)
	}
	if got := getDuration("BAD_DURATION", 3*time.Second); got != 3*time.Second {
		t.Fatalf("getDuration() = %s, want 3s", got)
	}
	if got := getInt("BAD_INT", 42); got != 42 {
		t.Fatalf("getInt() = %d, want 42", got)
	}
	if got := getInt64("BAD_INT64", 64); got != 64 {
		t.Fatalf("getInt64() = %d, want 64", got)
	}
	if got := getFloat64("BAD_FLOAT64", 1.25); got != 1.25 {
		t.Fatalf("getFloat64() = %v, want 1.25", got)
	}
}

func TestLoadTrimsRequiredStringValues(t *testing.T) {
	clearConfigEnv(t)
	t.Setenv("DATABASE_URL", " postgres://judge:test@localhost:5432/judge ")
	t.Setenv("HTTP_PORT", " 8082 ")
	t.Setenv("JUDGE_RUNTIME", " runsc ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if strings.Contains(cfg.DatabaseURL, " ") {
		t.Fatalf("DatabaseURL = %q, want trimmed value", cfg.DatabaseURL)
	}
	if cfg.HTTPPort != "8082" {
		t.Fatalf("HTTPPort = %q, want 8082", cfg.HTTPPort)
	}
	if cfg.JudgeRuntime != "runsc" {
		t.Fatalf("JudgeRuntime = %q, want runsc", cfg.JudgeRuntime)
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()

	for _, key := range configEnvKeys {
		t.Setenv(key, "")
	}
	t.Setenv("JUDGE_API_TOKEN", "test-api-token")
	t.Setenv("JUDGE_CALLBACK_TOKEN", "test-callback-token")
	t.Setenv("JUDGE_CALLBACK_URL", "https://backend.example.test/callback")
}

func TestLoadRejectsDevelopmentTokensInEveryEnvironment(t *testing.T) {
	for _, env := range []string{"dev", "stage", "prod"} {
		for _, key := range []string{"JUDGE_API_TOKEN", "JUDGE_CALLBACK_TOKEN"} {
			clearConfigEnv(t)
			t.Setenv("APP_ENV", env)
			t.Setenv("DATABASE_URL", "postgres://judge:test@localhost/judge")
			value := "dev-judge-api-token"
			if key == "JUDGE_CALLBACK_TOKEN" {
				value = "dev-judge-callback-token"
			}
			t.Setenv(key, value)
			if _, err := Load(); err == nil {
				t.Fatalf("%s accepted default token in %s", key, env)
			}
		}
	}
}

func TestLoadRejectsInvalidCPUQuota(t *testing.T) {
	for _, value := range []string{"0", "-1", "NaN", "+Inf"} {
		clearConfigEnv(t)
		t.Setenv("DATABASE_URL", "postgres://judge:test@localhost/judge")
		t.Setenv("JUDGE_CPU_COUNT", value)
		if _, err := Load(); err == nil {
			t.Fatalf("accepted CPU quota %s", value)
		}
	}
}
