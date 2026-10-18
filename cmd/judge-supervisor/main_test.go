//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"code-fight-judge/internal/supervisor"
)

func killGroup(pid int) { _ = syscall.Kill(-pid, syscall.SIGKILL) }

func TestExecutionPreservesInputAndExitCodes(t *testing.T) {
	for _, code := range []string{"0", "124", "137"} {
		result := execute([]string{"sh", "-c", "cat; exit " + code}, strings.NewReader("input\x00\xff"), time.Second, 1024, nil, killGroup)
		if result.Error != "" || result.TimedOut || result.OutputExceeded || string(result.Stdout) != "input\x00\xff" {
			t.Fatalf("unexpected report: %#v", result)
		}
		if code == "124" && result.ExitCode != 124 || code == "137" && result.ExitCode != 137 {
			t.Fatalf("exit code = %d", result.ExitCode)
		}
	}
}

func TestTimeoutStopsTaskAndDescendants(t *testing.T) {
	started := time.Now()
	result := execute([]string{"sh", "-c", "sleep 20 & wait"}, nil, 50*time.Millisecond, 1024, nil, killGroup)
	if !result.TimedOut || result.DurationMS < 50 || time.Since(started) > 2*time.Second {
		t.Fatalf("timeout failed: %#v", result)
	}
}

func TestCombinedOutputLimitStopsFlood(t *testing.T) {
	result := execute([]string{"sh", "-c", "while :; do printf 0123456789; printf abcdefghij >&2; done"}, nil, 2*time.Second, 1024, nil, killGroup)
	if result.TimedOut || !result.OutputExceeded || len(result.Stdout)+len(result.Stderr) != 1024 {
		t.Fatalf("output limit failed: %#v", result)
	}
}

func TestExactOutputLimitIsAllowed(t *testing.T) {
	result := execute([]string{"sh", "-c", "printf 1234; printf 5678 >&2"}, nil, time.Second, 8, nil, killGroup)
	if result.OutputExceeded || result.ExitCode != 0 || len(result.Stdout)+len(result.Stderr) != 8 {
		t.Fatalf("unexpected report: %#v", result)
	}
}

func TestBackgroundOutputDoesNotExtendExecution(t *testing.T) {
	started := time.Now()
	result := execute([]string{"sh", "-c", "sleep 20 & printf ok"}, nil, time.Second, 1024, nil, killGroup)
	if result.TimedOut || result.ExitCode != 0 || string(result.Stdout) != "ok" || time.Since(started) > time.Second {
		t.Fatalf("unexpected report: %#v", result)
	}
}

func TestReportPreservesBinaryOutput(t *testing.T) {
	dir := t.TempDir()
	if err := writeReport(dir, supervisor.Result{Stdout: []byte{0, 255}}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, supervisor.ResultFile))
	if err != nil || !bytes.Contains(data, []byte(`"stdout":"AP8="`)) {
		t.Fatalf("report=%s err=%v", data, err)
	}
}

func TestSupervisorRefusesToRunOutsideContainerPID1(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "supervisor")
	if output, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	if err := exec.Command(bin, "-timeout", "1s", "-output-bytes", "1024", "--", "true").Run(); err == nil {
		t.Fatal("supervisor ran outside isolated root PID 1")
	}
}
