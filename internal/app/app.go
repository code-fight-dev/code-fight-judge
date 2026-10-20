package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"time"

	"code-fight-judge/internal/callback"
	"code-fight-judge/internal/config"
	apphttp "code-fight-judge/internal/http"
	"code-fight-judge/internal/languages"
	"code-fight-judge/internal/metrics"
	"code-fight-judge/internal/queue"
	"code-fight-judge/internal/runner"
	"code-fight-judge/internal/sandbox"
	"code-fight-judge/internal/storage"

	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	cfg        config.Config
	httpServer *http.Server
	db         dbCloser
	worker     workerRunner
}

type dbCloser interface {
	Close()
}

type dbPinger interface {
	Ping(ctx context.Context) error
}

type workerRunner interface {
	Run(ctx context.Context)
}

func New() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := storage.NewPostgresPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("init postgres: %w", err)
	}

	return newApp(cfg, db), nil
}

func newApp(cfg config.Config, db *pgxpool.Pool) *App {
	jobsRepo := storage.NewJobsRepo(db)
	judgeMetrics := metrics.NewJudgeMetrics()
	languageRegistry := languages.NewDefaultRegistry(languages.RuntimeImages{
		Go:     cfg.JudgeGoImage,
		Python: cfg.JudgePythonImage,
		CPP:    cfg.JudgeCPPImage,
		Node:   cfg.JudgeNodeImage,
	})
	jobsHandler := apphttp.NewJobsHandler(jobsRepo, languageRegistry, cfg, judgeMetrics)
	languagesHandler := apphttp.NewLanguagesHandler(languageRegistry)
	readiness := &readinessChecker{
		db:            db,
		images:        []string{cfg.JudgeGoImage, cfg.JudgePythonImage, cfg.JudgeCPPImage, cfg.JudgeNodeImage},
		checkDocker:   checkDockerVersion,
		inspectDocker: inspectDockerImage,
	}
	router := apphttp.NewRouter(cfg, jobsHandler, languagesHandler, readiness, judgeMetrics)

	executor := sandbox.NewRunscExecutor()

	runnerSvc := runner.NewService(cfg, executor, languageRegistry)
	callbackClient := callback.NewClient(10*time.Second, cfg.JudgeCallbackURL)

	worker := queue.NewWorker(
		jobsRepo,
		runnerSvc,
		callbackClient,
		judgeMetrics,
		2*time.Second,
		cfg.JudgeCallbackAttempts,
		cfg.JudgeCallbackBackoff,
	)

	server := &http.Server{
		Addr:         cfg.Address(),
		Handler:      router,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	return &App{
		cfg:        cfg,
		httpServer: server,
		db:         db,
		worker:     worker,
	}
}

type readinessChecker struct {
	mu            sync.Mutex
	checks        map[string]string
	checkedAt     time.Time
	db            dbPinger
	images        []string
	checkDocker   func(ctx context.Context) error
	inspectDocker func(ctx context.Context, image string) error
}

func (c *readinessChecker) CheckReady(ctx context.Context) map[string]string {
	// Share one bounded probe across requests, including failed probes.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.checks != nil && time.Since(c.checkedAt) < 5*time.Second {
		return maps.Clone(c.checks)
	}
	// Client disconnects must not invalidate the cache and trigger another probe.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	checks := c.check(probeCtx)
	c.checks, c.checkedAt = checks, time.Now()
	return maps.Clone(checks)
}

func (c *readinessChecker) check(ctx context.Context) map[string]string {
	checks := map[string]string{
		"db":     "ok",
		"docker": "ok",
		"images": "ok",
	}

	if err := c.db.Ping(ctx); err != nil {
		checks["db"] = err.Error()
	}

	if err := c.checkDocker(ctx); err != nil {
		checks["docker"] = err.Error()
	}

	for _, image := range c.images {
		image = strings.TrimSpace(image)
		if image == "" {
			checks["images"] = "empty image reference"
			continue
		}
		if err := c.inspectDocker(ctx, image); err != nil {
			checks["images"] = "missing " + image + ": " + err.Error()
			break
		}
	}

	return checks
}

func checkDockerVersion(ctx context.Context) error {
	return exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").Run()
}

func inspectDockerImage(ctx context.Context, image string) error {
	return exec.CommandContext(ctx, "docker", "image", "inspect", image).Run()
}

func (a *App) Run(ctx context.Context) error {
	defer a.db.Close()
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	defer func() {
		stopWorker()
		select {
		case <-workerDone:
		case <-time.After(a.cfg.ShutdownTimeout):
		}
	}()

	errCh := make(chan error, 1)

	go func() { defer close(workerDone); a.worker.Run(workerCtx) }()

	go func() {
		log.Printf("judge service listening on %s", a.cfg.Address())
		if err := a.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("listen and serve: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.cfg.ShutdownTimeout)
		defer cancel()

		log.Printf("shutdown signal received")
		if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}

		select {
		case err := <-errCh:
			return err
		case <-time.After(200 * time.Millisecond):
			return nil
		}

	case err := <-errCh:
		return err
	}
}
