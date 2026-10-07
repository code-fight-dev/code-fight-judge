//go:build integration

package runner

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"code-fight-judge/internal/config"
	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/languages"
	"code-fight-judge/internal/sandbox"
)

func TestRunnerIntegrationRealSandboxPythonAccepted(t *testing.T) {
	cfg, registry := realSandboxIntegrationConfig(t)
	service := NewService(cfg, sandbox.NewRunscExecutor(), registry)

	result, err := service.Run(context.Background(), &jobs.Job{
		ID:              "runner-it-python",
		Language:        languages.PythonLanguage,
		LanguageVersion: languages.PythonVersion,
		SourceCode:      "def solve(input_data):\n    return input_data.strip().upper()\n",
		Limits: jobs.Limits{
			TimeMS:   2000,
			MemoryMB: 128,
			OutputKB: 64,
		},
		Tests: []jobs.TestCase{
			{
				ID:             "case-1",
				Input:          "hello\n",
				ExpectedOutput: "HELLO",
				Points:         10,
			},
			{
				ID:             "case-2",
				Input:          "judge\n",
				ExpectedOutput: "JUDGE",
				Points:         15,
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Verdict != "accepted" {
		t.Fatalf("Run().Verdict = %q, want accepted: stderr=%q stdout=%q", result.Verdict, result.Stderr, result.Stdout)
	}
	if result.PassedTests != 2 || result.TotalTests != 2 || result.Score != 25 {
		t.Fatalf("Run() passed/total/score = %d/%d/%d, want 2/2/25", result.PassedTests, result.TotalTests, result.Score)
	}
	if len(result.TestResults) != 2 {
		t.Fatalf("Run().TestResults len = %d, want 2", len(result.TestResults))
	}
}

func TestRunnerIntegrationRealSandboxGoCompileAndRun(t *testing.T) {
	cfg, registry := realSandboxIntegrationConfig(t)
	service := NewService(cfg, sandbox.NewRunscExecutor(), registry)

	result, err := service.Run(context.Background(), &jobs.Job{
		ID:              "runner-it-go",
		Language:        languages.GoLanguage,
		LanguageVersion: languages.GoVersion,
		SourceCode: `package main

import "strings"

func Solve(input string) string {
	return strings.TrimSpace(input) + "-ok"
}


`,
		Limits: jobs.Limits{
			TimeMS:   3000,
			MemoryMB: 128,
			OutputKB: 64,
		},
		Tests: []jobs.TestCase{
			{
				ID:             "case-1",
				Input:          "go\n",
				ExpectedOutput: "go-ok",
				Points:         1,
			},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Verdict != "accepted" {
		t.Fatalf("Run().Verdict = %q, want accepted: stderr=%q stdout=%q", result.Verdict, result.Stderr, result.Stdout)
	}
	if result.CompileTimeMS <= 0 {
		t.Fatalf("Run().CompileTimeMS = %d, want > 0", result.CompileTimeMS)
	}
	if result.PassedTests != 1 || result.Score != 1 {
		t.Fatalf("Run() passed/score = %d/%d, want 1/1", result.PassedTests, result.Score)
	}
}

func TestRunnerIntegrationSupervisorCannotBeSignaledOrOverwritten(t *testing.T) {
	runSecurityCase(t, `import os, signal
def solve(input_data):
    if os.getuid() != 10001 or os.getgid() != 10001:
        return "unsafe identity"
    with open("/proc/self/status") as status:
        fields = dict(line.split(":", 1) for line in status if ":" in line)
    if any(int(fields.get(key, "0").strip(), 16) for key in ("CapEff", "CapPrm", "CapAmb")):
        return "unsafe capabilities"
    try:
        os.kill(1, signal.SIGKILL)
        return "unsafe signal"
    except PermissionError:
        pass
    try:
        with open("/judge-report/result.json", "w") as report:
            report.write("{}")
        return "unsafe report"
    except PermissionError:
        pass
    return "ok"
`, 2000, 128, 1, "accepted")
}

func TestRunnerIntegrationTrustedTimeout(t *testing.T) {
	result := runSecurityCase(t, "import time\ndef solve(input_data):\n    time.sleep(20)\n    return 'ok'\n", 100, 128, 1, "time_limit_exceeded")
	if result.RunTimeMS < 100 || result.RunTimeMS > 2000 {
		t.Fatalf("unexpected task time: %d", result.RunTimeMS)
	}
}

func TestRunnerIntegrationOutputFlood(t *testing.T) {
	result := runSecurityCase(t, "import sys\ndef solve(input_data):\n    while True:\n        sys.stdout.write('x' * 4096)\n", 2000, 128, 1, "output_limit_exceeded")
	if len(result.Stdout)+len(result.Stderr) > 1024 {
		t.Fatal("output budget exceeded")
	}
}

func TestRunnerIntegrationDetachedDescendantCannotHoldOutputOpen(t *testing.T) {
	runSecurityCase(t, "import os, time\ndef solve(input_data):\n    if os.fork() == 0:\n        os.setsid()\n        time.sleep(20)\n        os._exit(0)\n    return 'ok'\n", 2000, 128, 1, "accepted")
}

func TestRunnerIntegrationOOM(t *testing.T) {
	runSecurityCase(t, "def solve(input_data):\n    data = bytearray(256 * 1024 * 1024)\n    return str(len(data))\n", 3000, 128, 1, "memory_limit_exceeded")
}

func runSecurityCase(t *testing.T, source string, timeMS, memoryMB, outputKB int, verdict string) *RunResult {
	t.Helper()
	cfg, registry := realSandboxIntegrationConfig(t)
	service := NewService(cfg, sandbox.NewRunscExecutor(), registry)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := service.Run(ctx, &jobs.Job{ID: "security-integration", Language: languages.PythonLanguage, LanguageVersion: languages.PythonVersion,
		SourceCode: source, Limits: jobs.Limits{TimeMS: timeMS, MemoryMB: memoryMB, OutputKB: outputKB}, Tests: []jobs.TestCase{{ID: "case", Input: "input", ExpectedOutput: "ok", Points: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != verdict {
		t.Fatalf("verdict=%s want=%s stderr=%q", result.Verdict, verdict, result.Stderr)
	}
	return result
}

func realSandboxIntegrationConfig(t *testing.T) (config.Config, *languages.Registry) {
	t.Helper()

	if strings.TrimSpace(os.Getenv("JUDGE_SANDBOX_INTEGRATION")) != "1" {
		t.Skip("set JUDGE_SANDBOX_INTEGRATION=1 to run real Docker/runsc runner integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	requireDocker(t, ctx)

	runtimeName := getEnv("JUDGE_INTEGRATION_RUNTIME", "runsc")
	requireDockerRuntime(t, ctx, runtimeName)

	goImage := getEnv("JUDGE_INTEGRATION_GO_IMAGE", "judge-go:1.24")
	pythonImage := getEnv("JUDGE_INTEGRATION_PYTHON_IMAGE", "judge-python:3.12")
	requireDockerImage(t, ctx, goImage)
	requireDockerImage(t, ctx, pythonImage)

	cfg := config.Config{
		JudgeWorkDir:         t.TempDir(),
		JudgeRuntime:         runtimeName,
		JudgeCPUCount:        1,
		JudgeDefaultMemoryMB: 256,
		JudgeMaxMemoryMB:     1024,
		JudgeMaxTimeMS:       10000,
		JudgeMaxOutputKB:     256,
		JudgeDefaultPids:     64,
		JudgeDefaultTmpfsMB:  64,
		JudgeMaxOutputDirMB:  128,
	}
	registry := languages.NewDefaultRegistry(languages.RuntimeImages{
		Go:     goImage,
		Python: pythonImage,
	})

	return cfg, registry
}

func requireDocker(t *testing.T, ctx context.Context) {
	t.Helper()

	output, err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").CombinedOutput()
	if err != nil {
		t.Fatalf("docker is not available: %v; output=%s", err, output)
	}
}

func requireDockerRuntime(t *testing.T, ctx context.Context, runtimeName string) {
	t.Helper()

	if strings.TrimSpace(runtimeName) == "" {
		return
	}

	output, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{json .Runtimes}}").CombinedOutput()
	if err != nil {
		t.Fatalf("docker runtimes are not available: %v; output=%s", err, output)
	}
	if !strings.Contains(string(output), `"`+runtimeName+`"`) {
		t.Fatalf("docker runtime %q is not available; runtimes=%s", runtimeName, output)
	}
}

func requireDockerImage(t *testing.T, ctx context.Context, image string) {
	t.Helper()

	output, err := exec.CommandContext(ctx, "docker", "image", "inspect", image).CombinedOutput()
	if err != nil {
		t.Fatalf("docker image %q is not available; build runtime images first: %v; output=%s", image, err, output)
	}
}

func getEnv(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}

	return value
}
