package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv           string
	HTTPHost         string
	HTTPPort         string
	ReadTimeout      time.Duration
	WriteTimeout     time.Duration
	IdleTimeout      time.Duration
	ShutdownTimeout  time.Duration
	MaxBodyBytes     int64
	DatabaseURL      string
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
	}

	if strings.TrimSpace(cfg.HTTPPort) == "" {
		return Config{}, fmt.Errorf("HTTP_PORT is required")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL is required")
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