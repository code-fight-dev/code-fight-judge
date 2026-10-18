//go:build linux

// judge-supervisor is PID 1 in a dedicated sandbox. Only this process retains
// the capabilities needed to change identity and stop the unprivileged task.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"code-fight-judge/internal/supervisor"
)

func main() {
	timeout := flag.Duration("timeout", 0, "task wall time limit")
	outputBytes := flag.Int("output-bytes", 0, "combined stdout/stderr limit")
	reportDir := flag.String("report-dir", "/judge-report", "trusted report directory")
	flag.Parse()
	if os.Getpid() != 1 || os.Geteuid() != 0 || *timeout <= 0 || *outputBytes <= 0 || len(flag.Args()) == 0 {
		fmt.Fprintln(os.Stderr, "supervisor requires root PID 1, positive limits and a command")
		os.Exit(1)
	}
	input, err := os.Open("/judge-stdin")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open task input:", err)
		os.Exit(1)
	}
	result := execute(flag.Args(), input, *timeout, *outputBytes,
		&syscall.Credential{Uid: 10001, Gid: 10001, Groups: []uint32{10001}},
		func(pid int) { _ = syscall.Kill(-1, syscall.SIGKILL) })
	input.Close()
	if err := writeReport(*reportDir, result); err != nil {
		fmt.Fprintln(os.Stderr, "write supervisor report:", err)
		os.Exit(1)
	}
	// Preserve the peak until collection, but release resources even if the judge
	// crashes or Docker becomes unavailable before it can remove the container.
	time.Sleep(30 * time.Second)
}

func writeReport(dir string, result supervisor.Result) error {
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "result.tmp")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, supervisor.ResultFile))
}

// Explicit pipes allow Wait to finish when the task exits, even if a descendant
// kept stdout open. The supervisor kills all descendants before draining pipes.
func execute(command []string, stdin io.Reader, timeout time.Duration, limit int, credential *syscall.Credential, kill func(int)) supervisor.Result {
	result := supervisor.Result{Version: supervisor.ProtocolVersion, ExitCode: -1}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer stdoutR.Close()
	defer stdoutW.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer stderrR.Close()
	defer stderrW.Close()
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdoutW, stderrW
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Credential: credential}
	started := time.Now()
	if err := cmd.Start(); err != nil {
		result.Error = err.Error()
		return result
	}
	stdoutW.Close()
	stderrW.Close()
	output := &capturedOutput{limit: limit, exceeded: make(chan struct{})}
	var readers sync.WaitGroup
	readers.Add(2)
	go func() { defer readers.Done(); _, _ = io.Copy(output.writer(false), stdoutR) }()
	go func() { defer readers.Done(); _, _ = io.Copy(output.writer(true), stderrR) }()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var waitErr error
	select {
	case waitErr = <-done:
	case <-timer.C:
		// Prefer a task that already exited over a simultaneously ready timer.
		select {
		case waitErr = <-done:
		default:
			result.TimedOut = true
			kill(cmd.Process.Pid)
			waitErr = <-done
		}
	case <-output.exceeded:
		kill(cmd.Process.Pid)
		waitErr = <-done
	}
	elapsed := time.Since(started)
	result.DurationMS = elapsed.Milliseconds()
	// A heavily loaded sandbox may delay delivery of the timer. A completed
	// process still must not pass if the trusted elapsed time exceeds its limit.
	if elapsed > timeout {
		result.TimedOut = true
	}
	kill(cmd.Process.Pid)
	readers.Wait()
	var exitErr *exec.ExitError
	if cmd.ProcessState == nil || waitErr != nil && !errors.As(waitErr, &exitErr) {
		result.Error = fmt.Sprintf("wait for task: %v", waitErr)
		return result
	}
	result.ExitCode = cmd.ProcessState.ExitCode()
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		result.ExitCode = 128 + int(status.Signal())
	}
	result.Stdout, result.Stderr = output.stdout.Bytes(), output.stderr.Bytes()
	result.OutputExceeded = output.overflow
	return result
}

type capturedOutput struct {
	mu             sync.Mutex
	stdout, stderr bytes.Buffer
	limit, size    int
	exceeded       chan struct{}
	overflow       bool
}

type outputWriter struct {
	output *capturedOutput
	stderr bool
}

func (o *capturedOutput) writer(stderr bool) io.Writer { return outputWriter{o, stderr} }
func (w outputWriter) Write(p []byte) (int, error) {
	o := w.output
	o.mu.Lock()
	defer o.mu.Unlock()
	accepted := min(len(p), o.limit-o.size)
	if accepted > 0 {
		if w.stderr {
			o.stderr.Write(p[:accepted])
		} else {
			o.stdout.Write(p[:accepted])
		}
		o.size += accepted
	}
	if accepted < len(p) && !o.overflow {
		o.overflow = true
		close(o.exceeded)
	}
	return len(p), nil
}
