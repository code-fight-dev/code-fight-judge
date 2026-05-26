package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewRunscExecutorUsesDockerBinary(t *testing.T) {
	executor := NewRunscExecutor()

	if executor.dockerBin != "docker" {
		t.Fatalf("dockerBin = %q, want docker", executor.dockerBin)
	}
}

func TestExecuteValidatesRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		req     ExecuteRequest
		wantErr string
	}{
		{
			name:    "image",
			req:     ExecuteRequest{},
			wantErr: "image is required",
		},
		{
			name: "work dir",
			req: ExecuteRequest{
				Image: "judge-go:test",
			},
			wantErr: "work_dir_host is required",
		},
		{
			name: "output dir",
			req: ExecuteRequest{
				Image:       "judge-go:test",
				WorkDirHost: t.TempDir(),
			},
			wantErr: "output_dir_host is required",
		},
		{
			name: "command",
			req: ExecuteRequest{
				Image:         "judge-go:test",
				WorkDirHost:   t.TempDir(),
				OutputDirHost: t.TempDir(),
			},
			wantErr: "command is required",
		},
	}

	executor := NewRunscExecutor()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := executor.Execute(context.Background(), tt.req)
			if err == nil {
				t.Fatalf("Execute() error = nil, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Execute() error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestExecuteRunsContainerLifecycleWithFakeDocker(t *testing.T) {
	dockerBin := buildFakeDocker(t)
	t.Setenv("FAKE_DOCKER_STDOUT", "program stdout")
	t.Setenv("FAKE_DOCKER_STDERR", "program stderr")
	t.Setenv("FAKE_DOCKER_WAIT_EXIT", "7")
	t.Setenv("FAKE_DOCKER_OOM", "true")
	t.Setenv("FAKE_DOCKER_STATS", "1MiB / 256MiB\n2MiB / 256MiB\n")

	executor := &RunscExecutor{dockerBin: dockerBin}

	result, err := executor.Execute(context.Background(), ExecuteRequest{
		JobID:             "job-1",
		Image:             "judge-go:test",
		WorkDirHost:       t.TempDir(),
		OutputDirHost:     t.TempDir(),
		OutputDirReadOnly: true,
		Command:           []string{"sh", "-c", "echo ok"},
		Stdin:             "input",
		CPUCount:          1.5,
		MemoryMB:          128,
		PidsLimit:         64,
		NetworkNone:       true,
		RuntimeName:       "runsc",
		ReadOnlyRoot:      true,
		TmpfsSizeMB:       32,
		Timeout:           time.Second,
		Env: map[string]string{
			"LANG": "C",
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.ExitCode != 7 {
		t.Fatalf("Execute().ExitCode = %d, want 7", result.ExitCode)
	}
	if result.Stdout != "program stdout" || result.Stderr != "program stderr" {
		t.Fatalf("Execute() logs = %q/%q, want fake logs", result.Stdout, result.Stderr)
	}
	if !result.OOMKilled {
		t.Fatal("Execute().OOMKilled = false, want true")
	}
	if result.PeakMemoryKB < 2048 {
		t.Fatalf("Execute().PeakMemoryKB = %d, want at least 2048", result.PeakMemoryKB)
	}
}

func TestExecuteReturnsTimedOutResultWhenWaitContextExpires(t *testing.T) {
	dockerBin := buildFakeDocker(t)
	t.Setenv("FAKE_DOCKER_WAIT_SLEEP", "200ms")
	t.Setenv("FAKE_DOCKER_STDOUT", "partial stdout")
	t.Setenv("FAKE_DOCKER_STDERR", "partial stderr")
	t.Setenv("FAKE_DOCKER_INSPECT_FAIL", "1")
	t.Setenv("FAKE_DOCKER_STATS", "3MiB / 256MiB\n")

	executor := &RunscExecutor{dockerBin: dockerBin}

	result, err := executor.Execute(context.Background(), ExecuteRequest{
		JobID:         "job-timeout",
		Image:         "judge-python:test",
		WorkDirHost:   t.TempDir(),
		OutputDirHost: t.TempDir(),
		Command:       []string{"python3", "main.py"},
		Timeout:       time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if !result.TimedOut {
		t.Fatal("Execute().TimedOut = false, want true")
	}
	if result.ExitCode != -1 {
		t.Fatalf("Execute().ExitCode = %d, want -1", result.ExitCode)
	}
	if result.Stdout != "partial stdout" || result.Stderr != "partial stderr" {
		t.Fatalf("Execute() logs = %q/%q, want partial logs", result.Stdout, result.Stderr)
	}
}

func TestCreateContainerErrors(t *testing.T) {
	dockerBin := buildFakeDocker(t)
	executor := &RunscExecutor{dockerBin: dockerBin}

	t.Run("empty id", func(t *testing.T) {
		t.Setenv("FAKE_DOCKER_CREATE_EMPTY", "1")
		_, err := executor.createContainer(context.Background(), []string{"create"})
		if err == nil || !strings.Contains(err.Error(), "empty container id") {
			t.Fatalf("createContainer() error = %v, want empty container id", err)
		}
	})

	t.Run("failure", func(t *testing.T) {
		t.Setenv("FAKE_DOCKER_CREATE_FAIL", "1")
		_, err := executor.createContainer(context.Background(), []string{"create"})
		if err == nil || !strings.Contains(err.Error(), "docker create failed") {
			t.Fatalf("createContainer() error = %v, want docker create failed", err)
		}
	})
}

func TestWaitContainerErrors(t *testing.T) {
	dockerBin := buildFakeDocker(t)
	executor := &RunscExecutor{dockerBin: dockerBin}

	t.Run("bad exit code", func(t *testing.T) {
		t.Setenv("FAKE_DOCKER_WAIT_EXIT", "not-a-number")
		_, err := executor.waitContainer(context.Background(), "container-id")
		if err == nil || !strings.Contains(err.Error(), "parse docker wait exit code") {
			t.Fatalf("waitContainer() error = %v, want parse error", err)
		}
	})

	t.Run("command failure", func(t *testing.T) {
		t.Setenv("FAKE_DOCKER_WAIT_FAIL", "1")
		_, err := executor.waitContainer(context.Background(), "container-id")
		if err == nil || !strings.Contains(err.Error(), "docker wait failed") {
			t.Fatalf("waitContainer() error = %v, want docker wait failed", err)
		}
	})
}

func TestRunscExecutorCommandHelpersWithFakeDocker(t *testing.T) {
	dockerBin := buildFakeDocker(t)
	executor := &RunscExecutor{dockerBin: dockerBin}

	if err := executor.startContainer(context.Background(), "container-id", "stdin"); err != nil {
		t.Fatalf("startContainer() error = %v", err)
	}

	t.Setenv("FAKE_DOCKER_STDOUT", "stdout")
	t.Setenv("FAKE_DOCKER_STDERR", "stderr")
	stdout, stderr := executor.readContainerLogs(context.Background(), "container-id")
	if stdout != "stdout" || stderr != "stderr" {
		t.Fatalf("readContainerLogs() = %q/%q, want stdout/stderr", stdout, stderr)
	}

	t.Setenv("FAKE_DOCKER_OOM", "false")
	if executor.inspectOOMKilled("container-id", 0) {
		t.Fatal("inspectOOMKilled(false, 0) = true, want false")
	}
	if !executor.inspectOOMKilled("container-id", 137) {
		t.Fatal("inspectOOMKilled(false, 137) = false, want true")
	}
	t.Setenv("FAKE_DOCKER_INSPECT_FAIL", "1")
	if !executor.inspectOOMKilled("container-id", 137) {
		t.Fatal("inspectOOMKilled(fail, 137) = false, want true")
	}
	if executor.inspectOOMKilled("container-id", 1) {
		t.Fatal("inspectOOMKilled(fail, 1) = true, want false")
	}

	if err := executor.forceRemoveContainer("container-id"); err != nil {
		t.Fatalf("forceRemoveContainer() error = %v", err)
	}
	if err := executor.forceKillContainer("container-id"); err != nil {
		t.Fatalf("forceKillContainer() error = %v", err)
	}
}

func TestStatsMonitorReadsStats(t *testing.T) {
	monitor := &statsMonitor{}
	samples := monitor.readStats(strings.NewReader("1MiB / 256MiB\nbad\n3MiB / 256MiB\n"))

	if samples != 2 {
		t.Fatalf("readStats() samples = %d, want 2", samples)
	}
	if monitor.peakKB != 3072 {
		t.Fatalf("readStats() peakKB = %d, want 3072", monitor.peakKB)
	}
}

func TestStatsMonitorStream(t *testing.T) {
	dockerBin := buildFakeDocker(t)
	t.Setenv("FAKE_DOCKER_STATS", "4MiB / 256MiB\n")

	monitor := &statsMonitor{}
	sampled := monitor.stream(&RunscExecutor{dockerBin: dockerBin}, context.Background(), "container-id")

	if !sampled {
		t.Fatal("stream() = false, want true")
	}
	if monitor.peakKB != 4096 {
		t.Fatalf("stream() peakKB = %d, want 4096", monitor.peakKB)
	}
}

func TestStatsMonitorStopReturnsPeak(t *testing.T) {
	done := make(chan struct{})
	close(done)
	monitor := &statsMonitor{
		cancel: func() {},
		done:   done,
		peakKB: 4096,
	}

	if got := monitor.Stop(); got != 4096 {
		t.Fatalf("Stop() = %d, want 4096", got)
	}
}

func TestParseDockerMemoryUsageKB(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{
			name: "bytes",
			in:   "512B / 256MiB",
			want: 1,
		},
		{
			name: "kib",
			in:   "1536KiB / 256MiB",
			want: 1536,
		},
		{
			name: "mib decimal",
			in:   "1.5MiB / 256MiB",
			want: 1536,
		},
		{
			name: "gib decimal",
			in:   "0.5GiB / 2GiB",
			want: 524288,
		},
		{
			name: "ansi escape prefix",
			in:   "\x1b[2J\x1b[H44.3MiB / 1GiB",
			want: 45364,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDockerMemoryUsageKB(tt.in)
			if err != nil {
				t.Fatalf("parseDockerMemoryUsageKB() error = %v", err)
			}

			if got != tt.want {
				t.Fatalf("parseDockerMemoryUsageKB() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseDockerMemoryUsageKBReturnsErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{
			name: "empty",
			in:   "",
		},
		{
			name: "bad number",
			in:   "not-a-numberMiB / 1GiB",
		},
		{
			name: "unknown unit",
			in:   "1TiB / 2TiB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseDockerMemoryUsageKB(tt.in)
			if err == nil {
				t.Fatal("parseDockerMemoryUsageKB() error = nil, want error")
			}
		})
	}
}

func TestStripANSIEscapeSequencesRemovesEscapesAndControlBytes(t *testing.T) {
	got := stripANSIEscapeSequences("\x1b[31mred\x1b[0m\x00\tok\n")

	if got != "red\tok\n" {
		t.Fatalf("stripANSIEscapeSequences() = %q, want red tab ok newline", got)
	}
}

func TestParseDockerBytesToKB(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{
			name: "empty unit",
			in:   "2048",
			want: 2,
		},
		{
			name: "kb",
			in:   "2KB",
			want: 2,
		},
		{
			name: "mb",
			in:   "2MB",
			want: 2048,
		},
		{
			name: "gb",
			in:   "1GB",
			want: 1048576,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDockerBytesToKB(tt.in)
			if err != nil {
				t.Fatalf("parseDockerBytesToKB() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseDockerBytesToKB() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestReadMemoryFileKB(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    int
		wantErr bool
	}{
		{
			name:    "rounds up",
			content: "1025\n",
			want:    2,
		},
		{
			name:    "empty",
			content: "",
			wantErr: true,
		},
		{
			name:    "max",
			content: "max",
			wantErr: true,
		},
		{
			name:    "invalid",
			content: "nope",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "memory.current")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatalf("write memory file: %v", err)
			}

			got, err := readMemoryFileKB(path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("readMemoryFileKB() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("readMemoryFileKB() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("readMemoryFileKB() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestReadCgroupPeakMemoryKBRejectsEmptyContainerID(t *testing.T) {
	executor := NewRunscExecutor()

	_, err := executor.readCgroupPeakMemoryKB(" ")
	if err == nil {
		t.Fatal("readCgroupPeakMemoryKB() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "empty container id") {
		t.Fatalf("readCgroupPeakMemoryKB() error = %q, want empty container id", err.Error())
	}
}

func TestMaxObservedMemoryKBFallsBackToStats(t *testing.T) {
	executor := NewRunscExecutor()

	if got := executor.maxObservedMemoryKB("missing-container", 123); got != 123 {
		t.Fatalf("maxObservedMemoryKB() = %d, want 123", got)
	}
}

func TestContainerNameHelpers(t *testing.T) {
	if got := buildContainerName(" job:abc/123 "); got != "judge-jobabc123" {
		t.Fatalf("buildContainerName() = %q, want judge-jobabc123", got)
	}

	longName := buildContainerName(strings.Repeat("a", 40))
	if len(longName) != len("judge-")+24 {
		t.Fatalf("long container name len = %d, want %d", len(longName), len("judge-")+24)
	}

	if got := sanitizeContainerNameSuffix("a/B_c-1!"); got != "aB_c-1" {
		t.Fatalf("sanitizeContainerNameSuffix() = %q, want aB_c-1", got)
	}

	generated := sanitizeContainerNameSuffix(" !!! ")
	if strings.TrimSpace(generated) == "" {
		t.Fatal("sanitizeContainerNameSuffix() generated empty suffix")
	}
}

func TestIsExitError(t *testing.T) {
	if !isExitError(&exec.ExitError{}) {
		t.Fatal("isExitError(exec.ExitError) = false, want true")
	}
	if isExitError(errors.New("plain error")) {
		t.Fatal("isExitError(plain error) = true, want false")
	}
}

func buildFakeDocker(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "fake_docker.go")
	exePath := filepath.Join(dir, "fake-docker")
	if runtime.GOOS == "windows" {
		exePath += ".exe"
	}

	source := `package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		os.Exit(0)
	}

	switch args[0] {
	case "create":
		if os.Getenv("FAKE_DOCKER_CREATE_FAIL") != "" {
			_, _ = fmt.Fprint(os.Stderr, "create failed")
			os.Exit(1)
		}
		if os.Getenv("FAKE_DOCKER_CREATE_EMPTY") != "" {
			return
		}
		fmt.Println("container-id")
	case "start":
		_, _ = io.Copy(io.Discard, os.Stdin)
		if os.Getenv("FAKE_DOCKER_START_FAIL") != "" {
			_, _ = fmt.Fprint(os.Stderr, "start failed")
			os.Exit(1)
		}
	case "wait":
		if sleep := os.Getenv("FAKE_DOCKER_WAIT_SLEEP"); sleep != "" {
			d, err := time.ParseDuration(sleep)
			if err == nil {
				time.Sleep(d)
			}
		}
		if os.Getenv("FAKE_DOCKER_WAIT_FAIL") != "" {
			_, _ = fmt.Fprint(os.Stderr, "wait failed")
			os.Exit(1)
		}
		exitCode := os.Getenv("FAKE_DOCKER_WAIT_EXIT")
		if exitCode == "" {
			exitCode = "0"
		}
		fmt.Println(exitCode)
	case "logs":
		_, _ = fmt.Fprint(os.Stdout, os.Getenv("FAKE_DOCKER_STDOUT"))
		_, _ = fmt.Fprint(os.Stderr, os.Getenv("FAKE_DOCKER_STDERR"))
	case "inspect":
		if os.Getenv("FAKE_DOCKER_INSPECT_FAIL") != "" {
			_, _ = fmt.Fprint(os.Stderr, "inspect failed")
			os.Exit(1)
		}
		oom := os.Getenv("FAKE_DOCKER_OOM")
		if oom == "" {
			oom = "false"
		}
		fmt.Println(oom)
	case "stats":
		stats := os.Getenv("FAKE_DOCKER_STATS")
		if stats == "" {
			stats = "1MiB / 256MiB\n"
		}
		_, _ = fmt.Fprint(os.Stdout, stats)
		if strings.Contains(stats, "SLEEP") {
			time.Sleep(time.Second)
		}
	case "rm", "kill":
	default:
	}
}
`
	if err := os.WriteFile(sourcePath, []byte(source), 0644); err != nil {
		t.Fatalf("write fake docker source: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", exePath, sourcePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fake docker: %v\n%s", err, output)
	}

	return exePath
}
