package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"code-fight-judge/internal/config"
	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/sandbox"
)

type Service struct {
	cfg      config.Config
	executor sandbox.Executor
}

func NewService(cfg config.Config, executor sandbox.Executor) *Service {
	return &Service{
		cfg:      cfg,
		executor: executor,
	}
}

func (s *Service) Run(ctx context.Context, job *jobs.Job) error {
	workDir, err := os.MkdirTemp(s.cfg.JudgeWorkDir, "judge-job-*")
	if err != nil {
		return fmt.Errorf("create temp workdir: %w", err)
	}
	defer os.RemoveAll(workDir)

	mainFile := filepath.Join(workDir, "main.go")
	if err := os.WriteFile(mainFile, []byte(job.SourceCode), 0644); err != nil {
		return fmt.Errorf("write source file: %w", err)
	}

	memoryMB := job.Limits.MemoryMB
	if memoryMB <= 0 {
		memoryMB = s.cfg.JudgeDefaultMemoryMB
	}

	_, err = s.executor.Execute(ctx, sandbox.ExecuteRequest{
		JobID:       job.ID,
		Image:       s.cfg.JudgeGoImage,
		WorkDirHost: workDir,
		Command:     []string{"sh", "-c", "go run /workspace/main.go"},

		CPUCount:    s.cfg.JudgeCPUCount,
		MemoryMB:    memoryMB,
		PidsLimit:   s.cfg.JudgeDefaultPids,
		NetworkNone: true,

		RuntimeName: s.cfg.JudgeRuntime,

		ReadOnlyRoot: false,
		TmpfsSizeMB:  s.cfg.JudgeDefaultTmpfsMB,
	})
	if err != nil {
		return fmt.Errorf("execute sandbox: %w", err)
	}

	return nil
}