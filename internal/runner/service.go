package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

type RunResult struct {
	ExitCode   int
	Stdout     string
	Stderr     string
	DurationMS int64

	Verdict     string
	PassedTests int
	TotalTests  int
	Score       int

	ErrorMessage string
}

func (s *Service) Run(ctx context.Context, job *jobs.Job) (*RunResult, error) {
	workDir, err := os.MkdirTemp(s.cfg.JudgeWorkDir, "judge-job-*")
	if err != nil {
		return nil, fmt.Errorf("create temp workdir: %w", err)
	}
	defer os.RemoveAll(workDir)

	if err := os.Chmod(workDir, 0755); err != nil {
		return nil, fmt.Errorf("chmod workdir: %w", err)
	}

	mainFile := filepath.Join(workDir, "main.go")
	if err := os.WriteFile(mainFile, []byte(job.SourceCode), 0644); err != nil {
		return nil, fmt.Errorf("write source file: %w", err)
	}

	memoryMB := job.Limits.MemoryMB
	if memoryMB <= 0 {
		memoryMB = s.cfg.JudgeDefaultMemoryMB
	}

	execResult, err := s.executor.Execute(ctx, sandbox.ExecuteRequest{
		JobID:       job.ID,
		Image:       s.cfg.JudgeGoImage,
		WorkDirHost: workDir,
		Command:     []string{"sh", "-c", "go run main.go"},

		CPUCount:    s.cfg.JudgeCPUCount,
		MemoryMB:    memoryMB,
		PidsLimit:   s.cfg.JudgeDefaultPids,
		NetworkNone: true,

		RuntimeName: s.cfg.JudgeRuntime,

		ReadOnlyRoot: false,
		TmpfsSizeMB:  s.cfg.JudgeDefaultTmpfsMB,
	})
	if err != nil {
		return nil, fmt.Errorf("execute sandbox: %w", err)
	}

	result := &RunResult{
		ExitCode:   execResult.ExitCode,
		Stdout:     execResult.Stdout,
		Stderr:     execResult.Stderr,
		DurationMS: execResult.DurationMS,
		TotalTests:  len(job.Tests),
	}

	if execResult.ExitCode != 0 {
		result.Verdict = "runtime_error"
		result.PassedTests = 0
		result.Score = 0
		result.ErrorMessage = "program exited with non-zero exit code"
		return result, nil
	}

	result.PassedTests, result.Score = evaluateTests(execResult.Stdout, job.Tests)

	if result.PassedTests == result.TotalTests {
		result.Verdict = "accepted"
	} else {
		result.Verdict = "wrong_answer"
	}

	return result, nil
}

func evaluateTests(stdout string, tests []jobs.TestCase) (passed int, score int) {
	for _, test := range tests {
		expected := normalizeOutput(test.ExpectedOutput)
		actual := normalizeOutput(stdout)

		if actual == expected {
			passed++
			score += test.Points
		}
	}

	return passed, score
}

func normalizeOutput(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.TrimSpace(value)

	return value
}