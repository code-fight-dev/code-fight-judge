package storage

import (
	"context"
	"strings"
	"testing"
)

func TestNewPostgresPoolRejectsInvalidURL(t *testing.T) {
	_, err := NewPostgresPool(context.Background(), "://bad-url")
	if err == nil {
		t.Fatal("NewPostgresPool() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "create pg pool") {
		t.Fatalf("NewPostgresPool() error = %q, want create pg pool", err.Error())
	}
}
