package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestWriteSourceFilesWrapsPythonSolveFunction(t *testing.T) {
	workspaceDir := t.TempDir()
	language := languages.Definition{
		Language:   languages.PythonLanguage,
		SourceFile: "main.py",
	}
	sourceCode := "def solve(input_data) -> str:\n    return input_data.strip()\n"

	if err := writeSourceFiles(workspaceDir, language, sourceCode); err != nil {
		t.Fatalf("writeSourceFiles() error = %v", err)
	}

	solution, err := os.ReadFile(filepath.Join(workspaceDir, "solution.py"))
	if err != nil {
		t.Fatalf("read solution.py: %v", err)
	}
	if string(solution) != sourceCode {
		t.Fatalf("solution.py = %q, want %q", string(solution), sourceCode)
	}

	mainFile, err := os.ReadFile(filepath.Join(workspaceDir, "main.py"))
	if err != nil {
		t.Fatalf("read main.py: %v", err)
	}
	mainSource := string(mainFile)
	for _, want := range []string{
		`exec(compile(source_file.read(), "/workspace/solution.py", "exec"), namespace)`,
		`solve = namespace.get("solve")`,
		`result = solve(sys.stdin.read())`,
		`sys.stdout.write(str(result))`,
	} {
		if !strings.Contains(mainSource, want) {
			t.Fatalf("main.py does not contain %q\n%s", want, mainSource)
		}
	}
}

func TestWriteSourceFilesWrapsGoSolveFunction(t *testing.T) {
	workspaceDir := t.TempDir()
	language := languages.Definition{
		Language:   languages.GoLanguage,
		SourceFile: "main.go",
	}
	sourceCode := "func Solve(input string) string {\n\treturn strings.TrimSpace(input)\n}\n"

	if err := writeSourceFiles(workspaceDir, language, sourceCode); err != nil {
		t.Fatalf("writeSourceFiles() error = %v", err)
	}

	solution, err := os.ReadFile(filepath.Join(workspaceDir, "solution.go"))
	if err != nil {
		t.Fatalf("read solution.go: %v", err)
	}
	solutionSource := string(solution)
	if !strings.HasPrefix(solutionSource, "package main\n\n") {
		t.Fatalf("solution.go missing package main prefix:\n%s", solutionSource)
	}
	if !strings.Contains(solutionSource, sourceCode) {
		t.Fatalf("solution.go does not contain source code:\n%s", solutionSource)
	}

	mainFile, err := os.ReadFile(filepath.Join(workspaceDir, "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	mainSource := string(mainFile)
	for _, want := range []string{
		`input, err := io.ReadAll(os.Stdin)`,
		`fmt.Print(Solve(string(input)))`,
	} {
		if !strings.Contains(mainSource, want) {
			t.Fatalf("main.go does not contain %q\n%s", want, mainSource)
		}
	}
}

func TestWriteSourceFilesKeepsGoMainProgram(t *testing.T) {
	workspaceDir := t.TempDir()
	language := languages.Definition{
		Language:   languages.GoLanguage,
		SourceFile: "main.go",
	}
	sourceCode := "package main\n\nfunc main() {}\n"

	if err := writeSourceFiles(workspaceDir, language, sourceCode); err != nil {
		t.Fatalf("writeSourceFiles() error = %v", err)
	}

	mainFile, err := os.ReadFile(filepath.Join(workspaceDir, "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if string(mainFile) != sourceCode {
		t.Fatalf("main.go = %q, want %q", string(mainFile), sourceCode)
	}

	if _, err := os.Stat(filepath.Join(workspaceDir, "solution.go")); !os.IsNotExist(err) {
		t.Fatalf("solution.go exists for full Go program, err=%v", err)
	}
}
