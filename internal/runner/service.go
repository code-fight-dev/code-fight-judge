package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/sandbox"
)

type Service struct {
	executor sandbox.Executor
}

func NewService(executor sandbox.Executor) *Service {
	return &Service{executor: executor}
}

func (s *Service) Run(ctx context.Context, job *jobs.Job) error {
	workDir, err := os.MkdirTemp("", "judge-job-*")
	if err != nil {
		return fmt.Errorf("create temp workdir: %w", err)
	}
	defer os.RemoveAll(workDir)

	mainFile := filepath.Join(workDir, "main.go")
	if err := os.WriteFile(mainFile, []byte(job.SourceCode), 0644); err != nil {
		return fmt.Errorf("write source file: %w", err)
	}

	_, err = s.executor.Execute(ctx, sandbox.ExecuteRequest{
		JobID:        job.ID,
		Image:        "judge-go:1.24",
		WorkDirHost:  workDir,
		Command:      []string{"sh", "-c", "go run /workspace/main.go"},
		CPUCount:     1,
		MemoryMB:     job.Limits.MemoryMB,
		PidsLimit:    64,
		ReadOnlyRoot: false,
		NetworkNone:  true,
		TmpfsSizeMB:  64,
		RuntimeName:  "runsc",
	})
	if err != nil {
		return err
	}

	return nil
}