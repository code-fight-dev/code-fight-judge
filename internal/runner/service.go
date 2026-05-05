package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"code-fight-judge/internal/config"
	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/languages"
	"code-fight-judge/internal/sandbox"
)

type Service struct {
	cfg       config.Config
	executor  sandbox.Executor
	languages *languages.Registry
}

func NewService(cfg config.Config, executor sandbox.Executor, languageRegistry *languages.Registry) *Service {
	return &Service{
		cfg:       cfg,
		executor:  executor,
		languages: languageRegistry,
	}
}

type RunResult struct {
	ExitCode   int
	Stdout     string
	Stderr     string
	DurationMS int64

	CompileTimeMS int
	RunTimeMS     int
	PeakMemoryKB  int

	Verdict     string
	PassedTests int
	TotalTests  int
	Score       int

	ErrorMessage string
	TestResults  []TestResult
}

type TestResult struct {
	TestCaseID     string
	Status         string
	TimeMS         int
	MemoryKB      int
	ExitCode      int
	CheckerMessage string
}

func (s *Service) Run(ctx context.Context, job *jobs.Job) (*RunResult, error) {
	language, ok := s.languages.Lookup(job.Language, job.LanguageVersion)
	if !ok {
		return &RunResult{
			ExitCode:     -1,
			Stdout:       "",
			Stderr:       "",
			DurationMS:   0,
			PeakMemoryKB: 0,
			Verdict:      "system_error",
			PassedTests:  0,
			TotalTests:   len(job.Tests),
			Score:        0,
			ErrorMessage: "language and language_version are not supported",
			TestResults:  []TestResult{},
		}, nil
	}

	jobDir, err := os.MkdirTemp(s.cfg.JudgeWorkDir, "judge-job-*")
	if err != nil {
		return nil, fmt.Errorf("create temp job dir: %w", err)
	}
	defer os.RemoveAll(jobDir)

	workspaceDir := filepath.Join(jobDir, "workspace")
	outputDir := filepath.Join(jobDir, "output")

	if err := os.MkdirAll(workspaceDir, 0755); err != nil {
		return nil, fmt.Errorf("create workspace dir: %w", err)
	}

	if err := os.MkdirAll(outputDir, 0777); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	if err := os.Chmod(workspaceDir, 0755); err != nil {
		return nil, fmt.Errorf("chmod workspace dir: %w", err)
	}

	if err := os.Chmod(outputDir, 0777); err != nil {
		return nil, fmt.Errorf("chmod output dir: %w", err)
	}

	mainFile := filepath.Join(workspaceDir, language.SourceFile)
	if err := os.WriteFile(mainFile, []byte(job.SourceCode), 0644); err != nil {
		return nil, fmt.Errorf("write source file: %w", err)
	}

	memoryMB := job.Limits.MemoryMB
	if memoryMB <= 0 {
		memoryMB = s.cfg.JudgeDefaultMemoryMB
	}

	totalTests := len(job.Tests)
	if totalTests == 0 {
		return &RunResult{
			ExitCode:     -1,
			Stdout:       "",
			Stderr:       "",
			DurationMS:   0,
			PeakMemoryKB: 0,
			Verdict:      "system_error",
			PassedTests:  0,
			TotalTests:   0,
			Score:        0,
			ErrorMessage: "no tests provided",
			TestResults:  []TestResult{},
		}, nil
	}

	compileResult, err := s.executor.Execute(ctx, sandbox.ExecuteRequest{
		JobID:         job.ID + "-compile",
		Image:         language.Image,
		WorkDirHost:   workspaceDir,
		OutputDirHost: outputDir,
		Command:       language.CompileCommand,

		CPUCount:    s.cfg.JudgeCPUCount,
		MemoryMB:    memoryMB,
		PidsLimit:   s.cfg.JudgeDefaultPids,
		NetworkNone: true,

		RuntimeName: s.cfg.JudgeRuntime,

		ReadOnlyRoot: true,
		TmpfsSizeMB:  s.cfg.JudgeDefaultTmpfsMB,
	})
	if err != nil {
		return nil, fmt.Errorf("compile sandbox: %w", err)
	}

	if compileResult.OOMKilled || compileResult.ExitCode == 137 {
		return &RunResult{
			ExitCode:      compileResult.ExitCode,
			Stdout:        compileResult.Stdout,
			Stderr:        compileResult.Stderr,
			DurationMS:    compileResult.DurationMS,
			CompileTimeMS: int(compileResult.DurationMS),
			RunTimeMS:     0,
			PeakMemoryKB:  0,
			Verdict:       "memory_limit_exceeded",
			PassedTests:   0,
			TotalTests:    totalTests,
			Score:         0,
			ErrorMessage:  "memory limit exceeded",
			TestResults:   []TestResult{},
		}, nil
	}

	if compileResult.ExitCode != 0 {
		return &RunResult{
			ExitCode:      compileResult.ExitCode,
			Stdout:        compileResult.Stdout,
			Stderr:        compileResult.Stderr,
			DurationMS:    compileResult.DurationMS,
			CompileTimeMS: int(compileResult.DurationMS),
			RunTimeMS:      0,
			PeakMemoryKB:  0,
			Verdict:       "compile_error",
			PassedTests:   0,
			TotalTests:    totalTests,
			Score:         0,
			ErrorMessage:  "compile failed",
			TestResults:   []TestResult{},
		}, nil
	}

	result := &RunResult{
		ExitCode:      0,
		Stdout:        "",
		Stderr:        "",
		DurationMS:    compileResult.DurationMS,
		CompileTimeMS: int(compileResult.DurationMS),
		RunTimeMS:      0,
		PeakMemoryKB:  0,

		Verdict:     "accepted",
		PassedTests: 0,
		TotalTests:  totalTests,
		Score:       0,
		TestResults: make([]TestResult, 0, totalTests),
	}

	for _, test := range job.Tests {
		runResult, err := s.executor.Execute(ctx, sandbox.ExecuteRequest{
			JobID:             fmt.Sprintf("%s-test-%s", job.ID, test.ID),
			Image:             language.Image,
			WorkDirHost:       workspaceDir,
			OutputDirHost:     outputDir,
			OutputDirReadOnly: true,
			Command:           language.RunCommand,
			Stdin:             test.Input,

			CPUCount:    s.cfg.JudgeCPUCount,
			MemoryMB:    memoryMB,
			PidsLimit:   s.cfg.JudgeDefaultPids,
			NetworkNone: true,

			RuntimeName: s.cfg.JudgeRuntime,

			ReadOnlyRoot: true,
			TmpfsSizeMB:  s.cfg.JudgeDefaultTmpfsMB,
			Timeout:      time.Duration(job.Limits.TimeMS) * time.Millisecond,
		})
		if err != nil {
			return nil, fmt.Errorf("run test sandbox: %w", err)
		}

		result.Stdout = runResult.Stdout
		result.Stderr = runResult.Stderr
		result.ExitCode = runResult.ExitCode
		result.DurationMS += runResult.DurationMS
		result.RunTimeMS += int(runResult.DurationMS)
		result.PeakMemoryKB = maxInt(result.PeakMemoryKB, runResult.PeakMemoryKB)

		testResult := TestResult{
			TestCaseID: test.ID,
			TimeMS:     int(runResult.DurationMS),
			MemoryKB:   runResult.PeakMemoryKB,
			ExitCode:   runResult.ExitCode,
		}

		if runResult.TimedOut {
			testResult.Status = "time_limit_exceeded"
			testResult.CheckerMessage = "time limit exceeded"

			result.Verdict = "time_limit_exceeded"
			result.ErrorMessage = "time limit exceeded"
			result.TestResults = append(result.TestResults, testResult)

			break
		}

		if runResult.OOMKilled || runResult.ExitCode == 137 {
			testResult.Status = "memory_limit_exceeded"
			testResult.CheckerMessage = "memory limit exceeded"

			result.Verdict = "memory_limit_exceeded"
			result.ErrorMessage = "memory limit exceeded"
			result.TestResults = append(result.TestResults, testResult)

			break
		}

		if runResult.ExitCode != 0 {
			testResult.Status = "runtime_error"
			testResult.CheckerMessage = "program exited with non-zero exit code"

			result.Verdict = "runtime_error"
			result.ErrorMessage = "program exited with non-zero exit code"
			result.TestResults = append(result.TestResults, testResult)

			break
		}

		if normalizeOutput(runResult.Stdout) == normalizeOutput(test.ExpectedOutput) {
			testResult.Status = "accepted"
			result.PassedTests++
			result.Score += test.Points
		} else {
			testResult.Status = "wrong_answer"
			testResult.CheckerMessage = "stdout does not match expected output"

			if result.Verdict == "accepted" {
				result.Verdict = "wrong_answer"
				result.ErrorMessage = "stdout does not match expected output"
			}
		}

		result.TestResults = append(result.TestResults, testResult)
	}

	return result, nil
}

func normalizeOutput(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.TrimSpace(value)

	return value
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}

	return right
}
