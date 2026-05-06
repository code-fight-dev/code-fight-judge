package runner

import (
	"context"
	"testing"

	"code-fight-judge/internal/config"
	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/languages"
	"code-fight-judge/internal/sandbox"
)

type fakeExecutor struct {
	requests []sandbox.ExecuteRequest
}

func (e *fakeExecutor) Execute(ctx context.Context, req sandbox.ExecuteRequest) (*sandbox.ExecuteResult, error) {
	e.requests = append(e.requests, req)

	return &sandbox.ExecuteResult{
		ExitCode: 0,
		Stdout:   "ok\n",
	}, nil
}

func TestRunSkipsCompileForPython(t *testing.T) {
	executor := &fakeExecutor{}
	registry := languages.NewDefaultRegistry(languages.RuntimeImages{
		Go:     "judge-go:test",
		Python: "judge-python:test",
	})
	service := NewService(config.Config{
		JudgeWorkDir:        t.TempDir(),
		JudgeRuntime:        "runsc",
		JudgeCPUCount:       1,
		JudgeDefaultPids:    64,
		JudgeDefaultTmpfsMB: 64,
	}, executor, registry)

	result, err := service.Run(context.Background(), &jobs.Job{
		ID:              "job-python",
		Language:        "python",
		LanguageVersion: "3.12",
		SourceCode:      "print('ok')\n",
		Limits: jobs.Limits{
			TimeMS:   1000,
			MemoryMB: 128,
			OutputKB: 64,
		},
		Tests: []jobs.TestCase{
			{
				ID:             "case-1",
				Input:          "",
				ExpectedOutput: "ok",
				Points:         1,
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Verdict != "accepted" {
		t.Fatalf("Run().Verdict = %q, want accepted", result.Verdict)
	}
	if result.CompileTimeMS != 0 {
		t.Fatalf("Run().CompileTimeMS = %d, want 0", result.CompileTimeMS)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.requests))
	}

	request := executor.requests[0]
	if request.Image != "judge-python:test" {
		t.Fatalf("run request Image = %q, want judge-python:test", request.Image)
	}
	if len(request.Command) == 0 {
		t.Fatal("run request Command is empty")
	}
	if request.Command[0] != "python3" {
		t.Fatalf("run request Command[0] = %q, want python3", request.Command[0])
	}
}
