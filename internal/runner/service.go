package runner

import (
	"context"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	MemoryKB       int
	ExitCode       int
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

	if err := writeSourceFiles(workspaceDir, language, job.SourceCode); err != nil {
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

	compileResult := &sandbox.ExecuteResult{}

	if len(language.CompileCommand) > 0 {
		compileResult, err = s.executor.Execute(ctx, sandbox.ExecuteRequest{
			JobID:         job.ID + "-compile",
			Image:         language.Image,
			WorkDirHost:   workspaceDir,
			OutputDirHost: outputDir,
			Command:       language.CompileCommand,

			CPUCount:    s.cfg.JudgeCPUCount,
			MemoryMB:    compileMemoryMB(s.cfg, memoryMB),
			PidsLimit:   s.cfg.JudgeDefaultPids,
			NetworkNone: true,

			RuntimeName: s.cfg.JudgeRuntime,

			ReadOnlyRoot: true,
			TmpfsSizeMB:  s.cfg.JudgeDefaultTmpfsMB,
			Timeout:      compileTimeout(s.cfg),
			Env:          restrictedRuntimeEnv(job.Language),
		})
		if err != nil {
			return nil, fmt.Errorf("compile sandbox: %w", err)
		}
	}

	if compileResult.TimedOut {
		return &RunResult{
			ExitCode:      compileResult.ExitCode,
			Stdout:        compileResult.Stdout,
			Stderr:        compileResult.Stderr,
			DurationMS:    compileResult.DurationMS,
			CompileTimeMS: int(compileResult.DurationMS),
			RunTimeMS:     0,
			PeakMemoryKB:  0,
			Verdict:       "time_limit_exceeded",
			PassedTests:   0,
			TotalTests:    totalTests,
			Score:         0,
			ErrorMessage:  "compile time limit exceeded",
			TestResults:   []TestResult{},
		}, nil
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
			RunTimeMS:     0,
			PeakMemoryKB:  0,
			Verdict:       "compile_error",
			PassedTests:   0,
			TotalTests:    totalTests,
			Score:         0,
			ErrorMessage:  "compile failed",
			TestResults:   []TestResult{},
		}, nil
	}

	if s.cfg.JudgeMaxOutputDirMB > 0 {
		outputKB, err := dirSizeKB(outputDir)
		if err != nil {
			return nil, fmt.Errorf("measure output dir: %w", err)
		}
		if outputKB > s.cfg.JudgeMaxOutputDirMB*1024 {
			return &RunResult{
				ExitCode:      -1,
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
				ErrorMessage:  "output directory limit exceeded",
				TestResults:   []TestResult{},
			}, nil
		}
	}

	result := &RunResult{
		ExitCode:      0,
		Stdout:        "",
		Stderr:        "",
		DurationMS:    compileResult.DurationMS,
		CompileTimeMS: int(compileResult.DurationMS),
		RunTimeMS:     0,
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
			Env:          restrictedRuntimeEnv(job.Language),
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

		if test.SkipOutputCheck {
			testResult.Status = "accepted"
			testResult.CheckerMessage = "output check skipped"
			result.PassedTests++
			result.Score += test.Points
		} else if normalizeOutput(runResult.Stdout) == normalizeOutput(test.ExpectedOutput) {
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

func restrictedRuntimeEnv(language string) map[string]string {
	env := map[string]string{
		"HOME":   "/tmp",
		"TMPDIR": "/tmp",
	}

	if languages.NormalizeLanguage(language) == languages.PythonLanguage {
		env["PYTHONNOUSERSITE"] = "1"
		env["PYTHONDONTWRITEBYTECODE"] = "1"
		env["PYTHONUNBUFFERED"] = "1"
	}

	return env
}

func compileMemoryMB(cfg config.Config, runMemoryMB int) int {
	if cfg.JudgeMaxMemoryMB > 0 {
		return cfg.JudgeMaxMemoryMB
	}
	if cfg.JudgeDefaultMemoryMB > 0 && cfg.JudgeDefaultMemoryMB > runMemoryMB {
		return cfg.JudgeDefaultMemoryMB
	}
	if runMemoryMB > 0 {
		return runMemoryMB
	}

	return 256
}

func compileTimeout(cfg config.Config) time.Duration {
	if cfg.JudgeMaxTimeMS > 0 {
		return time.Duration(cfg.JudgeMaxTimeMS) * time.Millisecond
	}

	return 0
}

func writeSourceFiles(workspaceDir string, language languages.Definition, sourceCode string) error {
	switch languages.NormalizeLanguage(language.Language) {
	case languages.GoLanguage:
		if hasGoMainFunction(sourceCode) {
			mainFile := filepath.Join(workspaceDir, language.SourceFile)
			return os.WriteFile(mainFile, []byte(sourceCode), 0644)
		}

		mainFile := filepath.Join(workspaceDir, language.SourceFile)
		return os.WriteFile(mainFile, []byte(buildGoSolveProgram(sourceCode)), 0644)

	case languages.PythonLanguage:
		solutionFile := filepath.Join(workspaceDir, "solution.py")
		if err := os.WriteFile(solutionFile, []byte(sourceCode), 0644); err != nil {
			return err
		}

		mainFile := filepath.Join(workspaceDir, language.SourceFile)
		return os.WriteFile(mainFile, []byte(pythonSolveHarness), 0644)

	default:
		mainFile := filepath.Join(workspaceDir, language.SourceFile)
		return os.WriteFile(mainFile, []byte(sourceCode), 0644)
	}
}

var goMainFunctionPattern = regexp.MustCompile(`(?m)\bfunc\s+main\s*\(`)

func hasGoMainFunction(sourceCode string) bool {
	return goMainFunctionPattern.MatchString(sourceCode)
}

func ensureGoPackage(sourceCode string) string {
	if strings.HasPrefix(strings.TrimSpace(sourceCode), "package ") {
		return sourceCode
	}

	return "package main\n\n" + sourceCode
}

func buildGoSolveProgram(sourceCode string) string {
	sourceCode = ensureGoPackage(sourceCode)
	sourceCode = ensureGoHarnessImports(sourceCode)
	sourceCode = strings.TrimRight(sourceCode, "\r\n\t ") + "\n\n" + goSolveMain

	formatted, err := format.Source([]byte(sourceCode))
	if err != nil {
		return sourceCode
	}

	return string(formatted)
}

func ensureGoHarnessImports(sourceCode string) string {
	const (
		fmtImport = "fmt"
		ioImport  = "io"
		osImport  = "os"
	)

	required := []string{fmtImport, ioImport, osImport}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "solution.go", sourceCode, parser.ParseComments)
	if err != nil {
		return sourceCode
	}

	existing := make(map[string]struct{}, len(file.Imports))
	for _, importSpec := range file.Imports {
		path, err := strconv.Unquote(importSpec.Path.Value)
		if err != nil {
			continue
		}
		existing[path] = struct{}{}
	}

	missing := make([]string, 0, len(required))
	for _, path := range required {
		if _, ok := existing[path]; !ok {
			missing = append(missing, path)
		}
	}
	if len(missing) == 0 {
		return sourceCode
	}

	insertLines := make([]string, 0, len(missing))
	for _, path := range missing {
		insertLines = append(insertLines, fmt.Sprintf("\t%q\n", path))
	}
	insertText := strings.Join(insertLines, "")

	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.IMPORT {
			continue
		}

		if genDecl.Lparen.IsValid() {
			offset := fset.Position(genDecl.Rparen).Offset
			return sourceCode[:offset] + insertText + sourceCode[offset:]
		}

		start := fset.Position(genDecl.Pos()).Offset
		end := fset.Position(genDecl.End()).Offset
		importText := strings.TrimSpace(sourceCode[start:end])
		importText = strings.TrimSpace(strings.TrimPrefix(importText, "import"))
		replacement := "import (\n\t" + importText + "\n" + insertText + ")"

		return sourceCode[:start] + replacement + sourceCode[end:]
	}

	offset := fset.Position(file.Name.End()).Offset
	return sourceCode[:offset] + "\n\nimport (\n" + insertText + ")" + sourceCode[offset:]
}

const goSolveMain = `func main() {
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		panic(err)
	}

	fmt.Print(Solve(string(input)))
}
`

const pythonSolveHarness = `import sys

namespace = {"__name__": "__submission__"}
with open("/workspace/solution.py", "r", encoding="utf-8") as source_file:
    exec(compile(source_file.read(), "/workspace/solution.py", "exec"), namespace)

solve = namespace.get("solve")
if not callable(solve):
    raise TypeError("solve(input_data) is not defined")

result = solve(sys.stdin.read())
if result is not None:
    sys.stdout.write(str(result))
`

func dirSizeKB(root string) (int, error) {
	var total int64

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, err
	}

	return int((total + 1023) / 1024), nil
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
