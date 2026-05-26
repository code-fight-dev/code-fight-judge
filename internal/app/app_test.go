package app

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"code-fight-judge/internal/config"
)

type fakeDB struct {
	pingErr error
	closed  bool
}

func (db *fakeDB) Ping(ctx context.Context) error {
	return db.pingErr
}

func (db *fakeDB) Close() {
	db.closed = true
}

type fakeWorker struct {
	ran chan struct{}
}

func (w *fakeWorker) Run(ctx context.Context) {
	if w.ran != nil {
		close(w.ran)
	}
	<-ctx.Done()
}

func TestReadinessCheckerReportsOK(t *testing.T) {
	checker := readinessChecker{
		db:     &fakeDB{},
		images: []string{"judge-go:test", " judge-python:test "},
		checkDocker: func(ctx context.Context) error {
			return nil
		},
		inspectDocker: func(ctx context.Context, image string) error {
			return nil
		},
	}

	checks := checker.CheckReady(context.Background())

	for key, got := range checks {
		if got != "ok" {
			t.Fatalf("check %s = %q, want ok", key, got)
		}
	}
}

func TestNewAppBuildsWiring(t *testing.T) {
	cfg := config.Config{
		HTTPHost:              "127.0.0.1",
		HTTPPort:              "8081",
		ReadTimeout:           time.Second,
		WriteTimeout:          2 * time.Second,
		IdleTimeout:           3 * time.Second,
		ShutdownTimeout:       time.Second,
		MaxBodyBytes:          1 << 20,
		JudgeAPIToken:         "judge-token",
		JudgeGoImage:          "judge-go:test",
		JudgePythonImage:      "judge-python:test",
		JudgeWorkDir:          t.TempDir(),
		JudgeRuntime:          "runsc",
		JudgeCPUCount:         1,
		JudgeDefaultMemoryMB:  256,
		JudgeDefaultPids:      64,
		JudgeDefaultTmpfsMB:   64,
		JudgeMaxTimeMS:        10000,
		JudgeMaxMemoryMB:      1024,
		JudgeMaxOutputKB:      256,
		JudgeMaxTests:         128,
		JudgeMaxTestBytes:     1 << 20,
		JudgeCallbackAttempts: 3,
		JudgeCallbackBackoff:  time.Millisecond,
	}

	app := newApp(cfg, nil)

	if app == nil {
		t.Fatal("newApp() = nil")
	}
	if app.httpServer == nil || app.httpServer.Handler == nil {
		t.Fatal("newApp() did not configure http server")
	}
	if app.httpServer.Addr != "127.0.0.1:8081" {
		t.Fatalf("server Addr = %q, want 127.0.0.1:8081", app.httpServer.Addr)
	}
	if app.httpServer.ReadTimeout != time.Second || app.httpServer.WriteTimeout != 2*time.Second {
		t.Fatalf("server timeouts = %s/%s, want 1s/2s", app.httpServer.ReadTimeout, app.httpServer.WriteTimeout)
	}
	if app.worker == nil {
		t.Fatal("newApp() worker = nil")
	}
}

func TestDockerCommandChecksReturnErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := checkDockerVersion(ctx); err == nil {
		t.Fatal("checkDockerVersion() error = nil, want error")
	}
	if err := inspectDockerImage(ctx, "judge-go:test"); err == nil {
		t.Fatal("inspectDockerImage() error = nil, want error")
	}
}

func TestReadinessCheckerReportsFailures(t *testing.T) {
	checker := readinessChecker{
		db:     &fakeDB{pingErr: errors.New("db down")},
		images: []string{"", "judge-go:test"},
		checkDocker: func(ctx context.Context) error {
			return errors.New("docker down")
		},
		inspectDocker: func(ctx context.Context, image string) error {
			return errors.New("image missing")
		},
	}

	checks := checker.CheckReady(context.Background())

	if checks["db"] != "db down" {
		t.Fatalf("db check = %q, want db down", checks["db"])
	}
	if checks["docker"] != "docker down" {
		t.Fatalf("docker check = %q, want docker down", checks["docker"])
	}
	if checks["images"] != "missing judge-go:test: image missing" {
		t.Fatalf("images check = %q, want missing image", checks["images"])
	}
}

func TestReadinessCheckerReportsEmptyImageReference(t *testing.T) {
	checker := readinessChecker{
		db:     &fakeDB{},
		images: []string{" "},
		checkDocker: func(ctx context.Context) error {
			return nil
		},
		inspectDocker: func(ctx context.Context, image string) error {
			t.Fatalf("inspectDocker called for empty image %q", image)
			return nil
		},
	}

	checks := checker.CheckReady(context.Background())

	if checks["images"] != "empty image reference" {
		t.Fatalf("images check = %q, want empty image reference", checks["images"])
	}
}

func TestAppRunShutsDownOnContextCancel(t *testing.T) {
	db := &fakeDB{}
	worker := &fakeWorker{ran: make(chan struct{})}
	server := &http.Server{
		Addr:    "127.0.0.1:0",
		Handler: http.NewServeMux(),
	}
	app := &App{
		cfg: config.Config{
			HTTPHost:        "127.0.0.1",
			HTTPPort:        "0",
			ShutdownTimeout: time.Second,
		},
		httpServer: server,
		db:         db,
		worker:     worker,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx)
	}()

	select {
	case <-worker.ran:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not return")
	}
	if !db.closed {
		t.Fatal("db was not closed")
	}
}

func TestAppRunReturnsListenError(t *testing.T) {
	db := &fakeDB{}
	worker := &fakeWorker{ran: make(chan struct{})}
	app := &App{
		cfg: config.Config{
			HTTPHost:        "127.0.0.1",
			HTTPPort:        "bad-port",
			ShutdownTimeout: time.Second,
		},
		httpServer: &http.Server{
			Addr: "127.0.0.1:bad-port",
		},
		db:     db,
		worker: worker,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := app.Run(ctx)
	if err == nil {
		t.Fatal("Run() error = nil, want listen error")
	}
	if !db.closed {
		t.Fatal("db was not closed")
	}
}
