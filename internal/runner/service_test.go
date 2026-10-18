package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code-fight-judge/internal/config"
	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/languages"
	"code-fight-judge/internal/sandbox"
)

type fakeExecutor struct {
	requests []sandbox.ExecuteRequest
	results  []*sandbox.ExecuteResult
	err      error
	onRun    func(req sandbox.ExecuteRequest) error
}

func (e *fakeExecutor) Execute(ctx context.Context, req sandbox.ExecuteRequest) (*sandbox.ExecuteResult, error) {
	e.requests = append(e.requests, req)

	if e.onRun != nil {
		if err := e.onRun(req); err != nil {
			return nil, err
		}
	}
	if e.err != nil {
		return nil, e.err
	}

	if len(e.results) > 0 {
		result := e.results[0]
		e.results = e.results[1:]
		return result, nil
	}

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
	if got, want := request.Command[len(request.Command)-3:], []string{"python3", "-B", "/workspace/main.py"}; !stringSlicesEqual(got, want) {
		t.Fatalf("run command target = %#v, want %#v", got, want)
	}
}

func TestRunUsesSeparateCompileAndTaskLimits(t *testing.T) {
	executor := &fakeExecutor{}
	registry := languages.NewDefaultRegistry(languages.RuntimeImages{
		Go:     "judge-go:test",
		Python: "judge-python:test",
	})
	service := NewService(config.Config{
		JudgeWorkDir:         t.TempDir(),
		JudgeRuntime:         "runsc",
		JudgeCPUCount:        1,
		JudgeDefaultMemoryMB: 256,
		JudgeMaxMemoryMB:     1024,
		JudgeMaxTimeMS:       10000,
		JudgeCompileTimeout:  45 * time.Second,
		JudgeMaxOutputKB:     256,
		JudgeDefaultPids:     64,
		JudgeDefaultTmpfsMB:  64,
	}, executor, registry)

	result, err := service.Run(context.Background(), &jobs.Job{
		ID:              "job-go",
		Language:        "go",
		LanguageVersion: "1.24",
		SourceCode:      "package main\n\nfunc Solve(input string) string { return \"ok\" }\n",
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
	if len(executor.requests) != 2 {
		t.Fatalf("executor calls = %d, want compile + run", len(executor.requests))
	}
	if got, want := executor.requests[0].MemoryMB, 1024; got != want {
		t.Fatalf("compile MemoryMB = %d, want %d", got, want)
	}
	if got, want := executor.requests[0].Timeout, 45*time.Second; got != want {
		t.Fatalf("compile Timeout = %s, want %s", got, want)
	}
	if got, want := executor.requests[1].MemoryMB, 128; got != want {
		t.Fatalf("run MemoryMB = %d, want %d", got, want)
	}
	if got, want := executor.requests[1].Timeout, time.Second; got != want {
		t.Fatalf("run Timeout = %s, want %s", got, want)
	}
}

func TestCompileTimeoutDefaultsIndependentlyOfTaskLimits(t *testing.T) {
	for _, maxTimeMS := range []int{0, 100, 10000, 120000} {
		if got := compileTimeout(config.Config{JudgeMaxTimeMS: maxTimeMS}); got != time.Minute {
			t.Fatalf("task max=%dms: compile timeout=%s, want 1m", maxTimeMS, got)
		}
	}
}

func TestRunReportsCompileTimeout(t *testing.T) {
	executor := &fakeExecutor{
		results: []*sandbox.ExecuteResult{
			{
				ExitCode:   -1,
				DurationMS: 10000,
				TimedOut:   true,
			},
		},
	}
	registry := languages.NewDefaultRegistry(languages.RuntimeImages{
		Go:     "judge-go:test",
		Python: "judge-python:test",
	})
	service := NewService(config.Config{
		JudgeWorkDir:         t.TempDir(),
		JudgeRuntime:         "runsc",
		JudgeCPUCount:        1,
		JudgeDefaultMemoryMB: 256,
		JudgeMaxMemoryMB:     1024,
		JudgeMaxTimeMS:       10000,
		JudgeMaxOutputKB:     256,
		JudgeDefaultPids:     64,
		JudgeDefaultTmpfsMB:  64,
	}, executor, registry)

	result, err := service.Run(context.Background(), &jobs.Job{
		ID:              "job-go",
		Language:        "go",
		LanguageVersion: "1.24",
		SourceCode:      "package main\n\nfunc Solve(input string) string { return \"ok\" }\n",
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
	if result.Verdict != "time_limit_exceeded" {
		t.Fatalf("Run().Verdict = %q, want time_limit_exceeded", result.Verdict)
	}
	if result.ErrorMessage != "compile time limit exceeded" {
		t.Fatalf("Run().ErrorMessage = %q, want compile time limit exceeded", result.ErrorMessage)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("executor calls = %d, want only compile", len(executor.requests))
	}
}

func TestRunUsesTrustedSupervisorLimits(t *testing.T) {
	executor := &fakeExecutor{}
	service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))
	job := testPythonJob()
	job.Limits.TimeMS = 1500
	if _, err := service.Run(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	req := executor.requests[0]
	if !stringSlicesEqual(req.Command, []string{"python3", "-B", "/workspace/main.py"}) {
		t.Fatalf("unexpected command: %v", req.Command)
	}
	if req.Timeout != 1500*time.Millisecond || req.OutputKB != job.Limits.OutputKB {
		t.Fatalf("unexpected limits: %#v", req)
	}
}

func TestRunReportsUnsupportedLanguage(t *testing.T) {
	executor := &fakeExecutor{}
	service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))

	result, err := service.Run(context.Background(), &jobs.Job{
		ID:              "job-ruby",
		Language:        "ruby",
		LanguageVersion: "3.3",
		Tests: []jobs.TestCase{
			{ID: "case-1"},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Verdict != "system_error" {
		t.Fatalf("Run().Verdict = %q, want system_error", result.Verdict)
	}
	if result.ErrorMessage != "language and language_version are not supported" {
		t.Fatalf("Run().ErrorMessage = %q, want unsupported language message", result.ErrorMessage)
	}
	if len(executor.requests) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.requests))
	}
}

func TestRunReportsNoTestsProvided(t *testing.T) {
	executor := &fakeExecutor{}
	service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))

	result, err := service.Run(context.Background(), &jobs.Job{
		ID:              "job-python",
		Language:        "python",
		LanguageVersion: "3.12",
		SourceCode:      "def solve(input_data): return input_data\n",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Verdict != "system_error" {
		t.Fatalf("Run().Verdict = %q, want system_error", result.Verdict)
	}
	if result.ErrorMessage != "no tests provided" {
		t.Fatalf("Run().ErrorMessage = %q, want no tests provided", result.ErrorMessage)
	}
	if len(executor.requests) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(executor.requests))
	}
}

func TestRunReportsCompileErrors(t *testing.T) {
	tests := []struct {
		name        string
		compile     *sandbox.ExecuteResult
		wantVerdict string
		wantMessage string
	}{
		{
			name: "oom killed",
			compile: &sandbox.ExecuteResult{
				ExitCode:   137,
				OOMKilled:  true,
				DurationMS: 7,
			},
			wantVerdict: "memory_limit_exceeded",
			wantMessage: "memory limit exceeded",
		},
		{
			name: "non zero exit",
			compile: &sandbox.ExecuteResult{
				ExitCode:   2,
				Stderr:     "syntax error",
				DurationMS: 8,
			},
			wantVerdict: "compile_error",
			wantMessage: "compile failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &fakeExecutor{results: []*sandbox.ExecuteResult{tt.compile}}
			service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))

			result, err := service.Run(context.Background(), testGoJob())
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			if result.Verdict != tt.wantVerdict {
				t.Fatalf("Run().Verdict = %q, want %q", result.Verdict, tt.wantVerdict)
			}
			if result.ErrorMessage != tt.wantMessage {
				t.Fatalf("Run().ErrorMessage = %q, want %q", result.ErrorMessage, tt.wantMessage)
			}
			if len(executor.requests) != 1 {
				t.Fatalf("executor calls = %d, want only compile", len(executor.requests))
			}
		})
	}
}

func TestRunReportsOutputDirectoryLimitExceeded(t *testing.T) {
	executor := &fakeExecutor{
		onRun: func(req sandbox.ExecuteRequest) error {
			if !strings.HasSuffix(req.JobID, "-compile") {
				return nil
			}

			outputFile := filepath.Join(req.OutputDirHost, "large-artifact.bin")
			return os.WriteFile(outputFile, []byte(strings.Repeat("x", 2049)), 0644)
		},
	}
	cfg := testRunnerConfig(t)
	cfg.JudgeMaxOutputDirMB = 0
	service := NewService(cfg, executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))

	result, err := service.Run(context.Background(), testGoJob())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Verdict != "accepted" {
		t.Fatalf("Run().Verdict with disabled output dir limit = %q, want accepted", result.Verdict)
	}

	executor = &fakeExecutor{
		onRun: func(req sandbox.ExecuteRequest) error {
			if !strings.HasSuffix(req.JobID, "-compile") {
				return nil
			}

			outputFile := filepath.Join(req.OutputDirHost, "large-artifact.bin")
			return os.WriteFile(outputFile, []byte(strings.Repeat("x", 1024*1024+1)), 0644)
		},
	}
	cfg = testRunnerConfig(t)
	cfg.JudgeMaxOutputDirMB = 1
	service = NewService(cfg, executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))

	result, err = service.Run(context.Background(), testGoJob())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Verdict != "output_directory_limit_exceeded" {
		t.Fatalf("Run().Verdict = %q, want output_directory_limit_exceeded", result.Verdict)
	}
	if result.ErrorMessage != "output directory limit exceeded" {
		t.Fatalf("Run().ErrorMessage = %q, want output directory limit exceeded", result.ErrorMessage)
	}
}

func TestRunWrapsSandboxErrors(t *testing.T) {
	tests := []struct {
		name    string
		job     *jobs.Job
		results []*sandbox.ExecuteResult
		wantErr string
	}{
		{
			name:    "compile",
			job:     testGoJob(),
			wantErr: "compile sandbox",
		},
		{
			name: "run test",
			job:  testPythonJob(),
			results: []*sandbox.ExecuteResult{
				{ExitCode: 0},
			},
			wantErr: "run test sandbox",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &fakeExecutor{
				results: tt.results,
				err:     errors.New("sandbox unavailable"),
			}
			service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))

			_, err := service.Run(context.Background(), tt.job)
			if err == nil {
				t.Fatal("Run() error = nil, want sandbox error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Run() error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestRunReportsRuntimeFailures(t *testing.T) {
	tests := []struct {
		name        string
		run         *sandbox.ExecuteResult
		wantVerdict string
		wantMessage string
	}{
		{
			name: "sandbox timeout",
			run: &sandbox.ExecuteResult{
				ExitCode:   -1,
				DurationMS: 1000,
				TimedOut:   true,
			},
			wantVerdict: "time_limit_exceeded",
			wantMessage: "time limit exceeded",
		},
		{
			name: "oom killed",
			run: &sandbox.ExecuteResult{
				ExitCode:     137,
				DurationMS:   1000,
				OOMKilled:    true,
				PeakMemoryKB: 65536,
			},
			wantVerdict: "memory_limit_exceeded",
			wantMessage: "memory limit exceeded",
		},
		{
			name: "non zero exit",
			run: &sandbox.ExecuteResult{
				ExitCode:   2,
				Stderr:     "panic",
				DurationMS: 1000,
			},
			wantVerdict: "runtime_error",
			wantMessage: "program exited with non-zero exit code",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executor := &fakeExecutor{
				results: []*sandbox.ExecuteResult{
					tt.run,
				},
			}
			service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))

			result, err := service.Run(context.Background(), testPythonJob())
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			if result.Verdict != tt.wantVerdict {
				t.Fatalf("Run().Verdict = %q, want %q", result.Verdict, tt.wantVerdict)
			}
			if result.ErrorMessage != tt.wantMessage {
				t.Fatalf("Run().ErrorMessage = %q, want %q", result.ErrorMessage, tt.wantMessage)
			}
			if len(result.TestResults) != 1 || result.TestResults[0].Status != tt.wantVerdict {
				t.Fatalf("Run().TestResults = %#v, want one %q", result.TestResults, tt.wantVerdict)
			}
		})
	}
}

func TestRunContinuesAfterWrongAnswerAndScoresAcceptedTests(t *testing.T) {
	executor := &fakeExecutor{
		results: []*sandbox.ExecuteResult{
			{
				ExitCode:     0,
				Stdout:       "wrong\n",
				DurationMS:   10,
				PeakMemoryKB: 256,
			},
			{
				ExitCode:     0,
				Stdout:       "second ok\r\n",
				DurationMS:   20,
				PeakMemoryKB: 512,
			},
		},
	}
	service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))
	job := testPythonJob()
	job.Tests = []jobs.TestCase{
		{
			ID:             "case-1",
			ExpectedOutput: "first ok",
			Points:         40,
		},
		{
			ID:             "case-2",
			ExpectedOutput: "second ok",
			Points:         60,
		},
	}

	result, err := service.Run(context.Background(), job)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Verdict != "wrong_answer" {
		t.Fatalf("Run().Verdict = %q, want wrong_answer", result.Verdict)
	}
	if result.PassedTests != 1 || result.Score != 60 {
		t.Fatalf("Run() passed/score = %d/%d, want 1/60", result.PassedTests, result.Score)
	}
	if len(result.TestResults) != 2 {
		t.Fatalf("Run().TestResults len = %d, want 2", len(result.TestResults))
	}
	if result.TestResults[0].Status != "wrong_answer" || result.TestResults[1].Status != "accepted" {
		t.Fatalf("Run().TestResults statuses = %#v, want wrong_answer then accepted", result.TestResults)
	}
	if result.PeakMemoryKB != 512 {
		t.Fatalf("Run().PeakMemoryKB = %d, want 512", result.PeakMemoryKB)
	}
}

func TestRunSkipOutputCheckAcceptsAnyOutput(t *testing.T) {
	executor := &fakeExecutor{
		results: []*sandbox.ExecuteResult{
			{
				ExitCode:   0,
				Stdout:     "totally different",
				DurationMS: 10,
			},
		},
	}
	service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))
	job := testPythonJob()
	job.Tests[0].SkipOutputCheck = true
	job.Tests[0].Points = 25

	result, err := service.Run(context.Background(), job)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Verdict != "accepted" {
		t.Fatalf("Run().Verdict = %q, want accepted", result.Verdict)
	}
	if result.PassedTests != 1 || result.Score != 25 {
		t.Fatalf("Run() passed/score = %d/%d, want 1/25", result.PassedTests, result.Score)
	}
	if result.TestResults[0].CheckerMessage != "output check skipped" {
		t.Fatalf("checker message = %q, want output check skipped", result.TestResults[0].CheckerMessage)
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

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestWriteSourceFilesWrapsGoSolveFunction(t *testing.T) {
	workspaceDir := t.TempDir()
	language := languages.Definition{
		Language:   languages.GoLanguage,
		SourceFile: "main.go",
	}
	sourceCode := "import \"strings\"\n\nfunc Solve(input string) string {\n\treturn strings.TrimSpace(input)\n}\n"

	if err := writeSourceFiles(workspaceDir, language, sourceCode); err != nil {
		t.Fatalf("writeSourceFiles() error = %v", err)
	}

	mainFile, err := os.ReadFile(filepath.Join(workspaceDir, "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	mainSource := string(mainFile)
	for _, want := range []string{
		`package main`,
		`"fmt"`,
		`"io"`,
		`"os"`,
		`"strings"`,
		`func Solve(input string) string`,
		`input, err := io.ReadAll(os.Stdin)`,
		`fmt.Print(Solve(string(input)))`,
	} {
		if !strings.Contains(mainSource, want) {
			t.Fatalf("main.go does not contain %q\n%s", want, mainSource)
		}
	}

	if _, err := os.Stat(filepath.Join(workspaceDir, "solution.go")); !os.IsNotExist(err) {
		t.Fatalf("solution.go exists for wrapped Go program, err=%v", err)
	}
}

func TestWriteSourceFilesWrapsGoStarterImportsInSameFile(t *testing.T) {
	workspaceDir := t.TempDir()
	language := languages.Definition{
		Language:   languages.GoLanguage,
		SourceFile: "main.go",
	}
	sourceCode := `package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func Solve(input string) string {
	return strconv.Itoa(len(strings.TrimSpace(input)))
}
`

	if err := writeSourceFiles(workspaceDir, language, sourceCode); err != nil {
		t.Fatalf("writeSourceFiles() error = %v", err)
	}

	mainFile, err := os.ReadFile(filepath.Join(workspaceDir, "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	mainSource := string(mainFile)
	for _, want := range []string{
		`"fmt"`,
		`"io"`,
		`"os"`,
		`func Solve(input string) string`,
		`func main()`,
	} {
		if !strings.Contains(mainSource, want) {
			t.Fatalf("main.go does not contain %q\n%s", want, mainSource)
		}
	}
}

func TestWriteSourceFilesKeepsGoPackageAfterLeadingComments(t *testing.T) {
	workspaceDir := t.TempDir()
	language := languages.Definition{
		Language:   languages.GoLanguage,
		SourceFile: "main.go",
	}
	sourceCode := `// Trade Ledger Balance
// Parse stdin and return the answer as a string.
package main

import "strconv"

func Solve(input string) string {
	return strconv.Itoa(len(input))
}
`

	if err := writeSourceFiles(workspaceDir, language, sourceCode); err != nil {
		t.Fatalf("writeSourceFiles() error = %v", err)
	}

	mainFile, err := os.ReadFile(filepath.Join(workspaceDir, "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	mainSource := string(mainFile)
	if count := strings.Count(mainSource, "package main"); count != 1 {
		t.Fatalf("package main count = %d, want 1\n%s", count, mainSource)
	}
	for _, want := range []string{
		`"fmt"`,
		`"io"`,
		`"os"`,
		`"strconv"`,
		`func Solve(input string) string`,
		`func main()`,
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

func TestDirSizeKBAndNormalizeOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(strings.Repeat("a", 1024)), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}

	sizeKB, err := dirSizeKB(dir)
	if err != nil {
		t.Fatalf("dirSizeKB() error = %v", err)
	}
	if sizeKB != 2 {
		t.Fatalf("dirSizeKB() = %d, want 2", sizeKB)
	}

	if got := normalizeOutput(" ok\r\n\n "); got != "ok" {
		t.Fatalf("normalizeOutput() = %q, want ok", got)
	}
}

func TestRunnerHelperBranches(t *testing.T) {
	if got := compileMemoryMB(config.Config{}, 0); got != 256 {
		t.Fatalf("compileMemoryMB(empty, 0) = %d, want 256", got)
	}
	if got := compileMemoryMB(config.Config{JudgeDefaultMemoryMB: 512}, 128); got != 512 {
		t.Fatalf("compileMemoryMB(default 512, run 128) = %d, want 512", got)
	}
	if got := compileMemoryMB(config.Config{}, 128); got != 128 {
		t.Fatalf("compileMemoryMB(empty, run 128) = %d, want 128", got)
	}

	if got := maxInt(3, 7); got != 7 {
		t.Fatalf("maxInt(3, 7) = %d, want 7", got)
	}
}

func testRunnerConfig(t *testing.T) config.Config {
	t.Helper()

	return config.Config{
		JudgeWorkDir:         t.TempDir(),
		JudgeRuntime:         "runsc",
		JudgeCPUCount:        1,
		JudgeDefaultMemoryMB: 256,
		JudgeMaxMemoryMB:     1024,
		JudgeMaxTimeMS:       10000,
		JudgeMaxOutputKB:     256,
		JudgeDefaultPids:     64,
		JudgeDefaultTmpfsMB:  64,
		JudgeMaxOutputDirMB:  128,
	}
}

func testGoJob() *jobs.Job {
	return &jobs.Job{
		ID:              "job-go",
		Language:        "go",
		LanguageVersion: "1.24",
		SourceCode:      "package main\n\nfunc Solve(input string) string { return \"ok\" }\n",
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
	}
}

func testPythonJob() *jobs.Job {
	return &jobs.Job{
		ID:              "job-python",
		Language:        "python",
		LanguageVersion: "3.12",
		SourceCode:      "def solve(input_data): return input_data\n",
		Limits: jobs.Limits{
			TimeMS:   1000,
			MemoryMB: 128,
			OutputKB: 64,
		},
		Tests: []jobs.TestCase{
			{
				ID:             "case-1",
				Input:          "ok",
				ExpectedOutput: "ok",
				Points:         1,
			},
		},
	}
}

func TestRuntimeExitCodesAreNotResourceVerdicts(t *testing.T) {
	for _, code := range []int{124, 137} {
		executor := &fakeExecutor{results: []*sandbox.ExecuteResult{{ExitCode: code, DurationMS: 5}}}
		service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))
		result, err := service.Run(context.Background(), testPythonJob())
		if err != nil || result.Verdict != "runtime_error" {
			t.Fatalf("exit=%d result=%#v err=%v", code, result, err)
		}
	}
}

func TestRuntimeExceedingMeasuredTimeCannotPass(t *testing.T) {
	executor := &fakeExecutor{results: []*sandbox.ExecuteResult{{Stdout: "ok", DurationMS: 400}}}
	service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))
	job := testPythonJob()
	job.Limits.TimeMS = 100
	result, err := service.Run(context.Background(), job)
	if err != nil || result.Verdict != "time_limit_exceeded" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestOutputLimitCannotBeBypassedBySkippingChecker(t *testing.T) {
	executor := &fakeExecutor{results: []*sandbox.ExecuteResult{{Stdout: "partial", OutputExceeded: true}}}
	service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))
	job := testPythonJob()
	job.Tests[0].SkipOutputCheck = true
	result, err := service.Run(context.Background(), job)
	if err != nil || result.Verdict != "output_limit_exceeded" || result.Score != 0 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestMissingMemoryMeasurementMakesAggregateUnknown(t *testing.T) {
	executor := &fakeExecutor{results: []*sandbox.ExecuteResult{{Stdout: "ok", PeakMemoryKB: 1024}, {Stdout: "ok", PeakMemoryKB: 0}}}
	service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))
	job := testPythonJob()
	job.Tests = append(job.Tests, job.Tests[0])
	result, err := service.Run(context.Background(), job)
	if err != nil || result.PeakMemoryKB != 0 || result.TestResults[0].MemoryKB != 1024 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestCompileDiagnosticsSurviveSuccessfulExecution(t *testing.T) {
	executor := &fakeExecutor{results: []*sandbox.ExecuteResult{{Stderr: "compiler warning"}, {Stdout: "ok"}}}
	service := NewService(testRunnerConfig(t), executor, languages.NewDefaultRegistry(languages.RuntimeImages{}))
	result, err := service.Run(context.Background(), testGoJob())
	if err != nil || result.CompileLog != "compiler warning" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
