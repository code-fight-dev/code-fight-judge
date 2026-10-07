package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"code-fight-judge/internal/supervisor"
)

type RunscExecutor struct {
	dockerBin   string
	cgroupRoots []string
	procRoots   []string
}

func NewRunscExecutor() *RunscExecutor { return &RunscExecutor{dockerBin: "docker"} }

func (e *RunscExecutor) Execute(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error) {
	if req.Image == "" {
		return nil, fmt.Errorf("sandbox execute: image is required")
	}
	if req.WorkDirHost == "" {
		return nil, fmt.Errorf("sandbox execute: work_dir_host is required")
	}
	if req.OutputDirHost == "" {
		return nil, fmt.Errorf("sandbox execute: output_dir_host is required")
	}
	if len(req.Command) == 0 {
		return nil, fmt.Errorf("sandbox execute: command is required")
	}
	if req.Timeout <= 0 || req.OutputKB <= 0 || req.OutputKB > 1024*1024 {
		return nil, fmt.Errorf("sandbox execute: positive timeout and output limit (at most 1 GiB) are required")
	}
	if req.MemoryMB < 6 || req.CPUCount <= 0 || req.PidsLimit <= 0 {
		return nil, fmt.Errorf("sandbox execute: memory >= 6 MiB and positive CPU/PID limits are required")
	}
	// A regular read-only input file guarantees EOF without Docker attach's
	// OpenStdin/StdinOnce lifecycle and keeps input setup outside task timing.
	input, err := os.CreateTemp(filepath.Dir(req.WorkDirHost), "judge-stdin-*")
	if err != nil {
		return nil, fmt.Errorf("create task input: %w", err)
	}
	defer os.Remove(input.Name())
	if _, err := input.WriteString(req.Stdin); err != nil {
		input.Close()
		return nil, err
	}
	if err := input.Chmod(0444); err != nil {
		input.Close()
		return nil, err
	}
	if err := input.Close(); err != nil {
		return nil, err
	}
	runtimeName := req.RuntimeName
	if runtimeName == "" {
		runtimeName = "runsc"
	}
	name := buildContainerName(req.JobID)
	args := []string{
		"create", "--runtime=" + runtimeName, "--name", name,
		"--security-opt", "no-new-privileges", "--cap-drop", "ALL",
		// These capabilities belong only to root PID 1. Changing UID to 10001 clears
		// them in the task; no-new-privileges prevents regaining them via exec.
		"--cap-add", "SETUID", "--cap-add", "SETGID", "--cap-add", "KILL",
		"--user", "0:0", "--ipc", "none", "--log-driver", "none",
		"--entrypoint", "/usr/local/bin/judge-supervisor",
		"--ulimit", "nofile=256:256", "--ulimit", "fsize=134217728:134217728", "--ulimit", "nproc=128:128",
		"--cpus", strconv.FormatFloat(req.CPUCount, 'f', -1, 64),
		"--memory", fmt.Sprintf("%dm", req.MemoryMB),
		"--memory-swap", fmt.Sprintf("%dm", req.MemoryMB),
		"--pids-limit", strconv.Itoa(req.PidsLimit),
	}
	if req.NetworkNone {
		args = append(args, "--network", "none")
	}
	if req.ReadOnlyRoot {
		args = append(args, "--read-only")
	}
	tmpfsMB := req.TmpfsSizeMB
	if tmpfsMB <= 0 {
		tmpfsMB = 64
	}
	outputMode := "rw"
	if req.OutputDirReadOnly {
		outputMode = "ro"
	}
	args = append(args,
		"--tmpfs", fmt.Sprintf("/tmp:exec,size=%dm", tmpfsMB), "--tmpfs", "/gocache:exec,size=256m",
		"-e", "TMPDIR=/tmp", "-e", "GOTMPDIR=/tmp", "-e", "GOCACHE=/gocache",
		"-v", filepath.Clean(req.WorkDirHost)+":/workspace:ro",
		"-v", filepath.Clean(req.OutputDirHost)+":/output:"+outputMode,
		"-v", input.Name()+":/judge-stdin:ro",
		"--tmpfs", fmt.Sprintf("/judge-report:noexec,nosuid,nodev,mode=0700,uid=0,gid=0,size=%dk", req.OutputKB*2+64), "-w", "/workspace",
	)
	for k, v := range req.Env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, req.Image, "-timeout", req.Timeout.String(), "-output-bytes", strconv.Itoa(req.OutputKB*1024), "--")
	args = append(args, req.Command...)

	// This bounds infrastructure work, including create/start/report collection.
	// The task's own timer and measurements are controlled by trusted PID 1.
	runCtx, cancel := context.WithTimeout(ctx, req.Timeout+10*time.Second)
	defer cancel()
	id, err := e.createContainer(runCtx, args)
	if err != nil {
		_ = e.forceRemoveContainer(name) // create may have succeeded before CLI cancellation
		return nil, err
	}
	defer func() {
		if err := e.forceRemoveContainer(id); err != nil {
			log.Printf("sandbox cleanup failed container_id=%s err=%v", id, err)
		}
	}()
	log.Printf("sandbox execute: container created name=%s id=%s timeout=%s", name, id, req.Timeout)

	if err := e.startContainer(runCtx, id); err != nil {
		return nil, err
	}
	waitDone := make(chan containerWait, 1)
	go func() { code, err := e.waitContainer(runCtx, id); waitDone <- containerWait{code, err} }()
	defer func() {
		cancel()
		if waitDone != nil {
			<-waitDone
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		report, err := e.readSupervisorReport(runCtx, id, req.OutputKB*1024)
		if err == nil {
			if report.Error != "" {
				return nil, fmt.Errorf("sandbox supervisor: %s", report.Error)
			}
			peak, peakErr := e.readCgroupPeakMemoryKB(id)
			if peakErr != nil {
				log.Printf("sandbox memory: peak unavailable container_id=%s err=%v", id, peakErr)
			}
			oom, err := e.inspectOOMKilled(id)
			if err != nil {
				return nil, err
			}
			return &ExecuteResult{ExitCode: report.ExitCode, Stdout: string(report.Stdout), Stderr: string(report.Stderr),
				DurationMS: report.DurationMS, PeakMemoryKB: peak, TimedOut: report.TimedOut,
				OutputExceeded: report.OutputExceeded, OOMKilled: oom}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-runCtx.Done():
			return nil, fmt.Errorf("sandbox infrastructure deadline exceeded before trusted report: %w", runCtx.Err())
		case wait := <-waitDone:
			waitDone = nil
			if wait.err != nil {
				return nil, wait.err
			}
			// A successful supervisor stays alive after publishing its report. An exit
			// without a report is an infrastructure failure, unless Docker confirms OOM.
			oom, err := e.inspectOOMKilled(id)
			if err != nil {
				return nil, err
			}
			if !oom {
				return nil, fmt.Errorf("sandbox supervisor exited without report (exit %d)", wait.exitCode)
			}
			return &ExecuteResult{ExitCode: wait.exitCode, OOMKilled: true, TimeUnavailable: true}, nil
		case <-ticker.C:
		}
	}
}

type containerWait struct {
	exitCode int
	err      error
}

func (e *RunscExecutor) readSupervisorReport(ctx context.Context, id string, outputLimit int) (*supervisor.Result, error) {
	// The result is root-owned in a private tmpfs inside the sandbox. Reading via
	// a root docker exec avoids trusting host bind-mount UID mappings or allowing
	// the submission to tamper with a report owned by the judge's host UID.
	maxBytes := outputLimit*2 + 4096
	stdout, _, err := e.dockerCommandWithLimit(ctx, "", maxBytes+1, "exec", "--user", "0:0", id, "/bin/cat", "/judge-report/"+supervisor.ResultFile)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, os.ErrNotExist // missing report / container not started yet
	}
	return decodeSupervisorReport(strings.NewReader(stdout), outputLimit)
}

func decodeSupervisorReport(reader io.Reader, outputLimit int) (*supervisor.Result, error) {
	// Output uses base64, and report fields have a separate bounded allowance.
	maxBytes := int64(outputLimit)*2 + 4096
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("supervisor report exceeds maximum")
	}
	var result supervisor.Result
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("decode supervisor report: %w", err)
	}
	if result.Version != supervisor.ProtocolVersion || len(result.Stdout)+len(result.Stderr) > outputLimit || result.DurationMS < 0 {
		return nil, fmt.Errorf("invalid supervisor report")
	}
	return &result, nil
}

// Docker diagnostics are capped independently from task output.
type limitedBuffer struct {
	mu    sync.Mutex
	data  bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if remaining := b.limit - b.data.Len(); remaining > 0 {
		b.data.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}
func (b *limitedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }
func (e *RunscExecutor) dockerCommand(ctx context.Context, stdin string, args ...string) (string, string, error) {
	return e.dockerCommandWithLimit(ctx, stdin, 16384, args...)
}
func (e *RunscExecutor) dockerCommandWithLimit(ctx context.Context, stdin string, limit int, args ...string) (string, string, error) {
	stdout, stderr := &limitedBuffer{limit: limit}, &limitedBuffer{limit: 16384}
	cmd := exec.CommandContext(ctx, e.dockerBin, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}
func (e *RunscExecutor) createContainer(ctx context.Context, args []string) (string, error) {
	stdout, stderr, err := e.dockerCommand(ctx, "", args...)
	if err != nil {
		return "", fmt.Errorf("sandbox execute: docker create failed: %w; stderr=%s", err, stderr)
	}
	id := strings.TrimSpace(stdout)
	if id == "" {
		return "", fmt.Errorf("sandbox execute: docker create returned empty container id")
	}
	return id, nil
}
func (e *RunscExecutor) startContainer(ctx context.Context, id string) error {
	_, stderr, err := e.dockerCommand(ctx, "", "start", id)
	if err != nil {
		return fmt.Errorf("sandbox execute: docker start failed: %w; stderr=%s", err, stderr)
	}
	return nil
}

func (e *RunscExecutor) waitContainer(ctx context.Context, id string) (int, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		stdout, stderr, err := e.dockerCommand(ctx, "", "inspect", "--format", "{{json .State}}", id)
		if err != nil {
			return -1, fmt.Errorf("inspect sandbox state: %w; stderr=%s", err, stderr)
		}
		var state struct {
			Status   string
			ExitCode int
		}
		if err := json.Unmarshal([]byte(stdout), &state); err != nil {
			return -1, fmt.Errorf("decode sandbox state: %w", err)
		}
		if state.Status == "exited" || state.Status == "dead" {
			return state.ExitCode, nil
		}
		if state.Status != "created" && state.Status != "running" {
			return -1, fmt.Errorf("unexpected sandbox state %q", state.Status)
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-ticker.C:
		}
	}
}
func (e *RunscExecutor) inspectOOMKilled(id string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stdout, stderr, err := e.dockerCommand(ctx, "", "inspect", "--format", "{{.State.OOMKilled}}", id)
	if err != nil {
		return false, fmt.Errorf("inspect sandbox OOM state: %w; stderr=%s", err, stderr)
	}
	value := strings.TrimSpace(stdout)
	if value != "true" && value != "false" {
		return false, fmt.Errorf("invalid sandbox OOM state %q", value)
	}
	return value == "true", nil
}

func (e *RunscExecutor) readCgroupPeakMemoryKB(id string) (int, error) {
	if strings.TrimSpace(id) == "" {
		return 0, fmt.Errorf("empty container id")
	}
	roots := e.cgroupRoots
	if len(roots) == 0 {
		roots = []string{"/host/sys/fs/cgroup", "/sys/fs/cgroup"}
	}
	files := []string{
		filepath.Join("system.slice", "docker-"+id+".scope", "memory.peak"),
		filepath.Join("docker", id, "memory.peak"),
		filepath.Join("memory", "system.slice", "docker-"+id+".scope", "memory.max_usage_in_bytes"),
		filepath.Join("memory", "docker", id, "memory.max_usage_in_bytes"),
	}
	// Resolve non-default cgroup parents from the actual host process, when proc
	// is available. Fall back to standard Docker paths, never to current usage.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	pid, _, err := e.dockerCommand(ctx, "", "inspect", "--format", "{{.State.Pid}}", id)
	if err == nil {
		procRoots := e.procRoots
		if len(procRoots) == 0 {
			procRoots = []string{"/host/proc", "/proc"}
		}
		for _, procRoot := range procRoots {
			raw, err := os.ReadFile(filepath.Join(procRoot, strings.TrimSpace(pid), "cgroup"))
			if err != nil {
				continue
			}
			files = append(cgroupPeakFiles(string(raw), id), files...)
		}
	}
	for _, root := range roots {
		for _, file := range files {
			if kb, err := readMemoryFileKB(filepath.Join(root, file)); err == nil {
				return kb, nil
			}
		}
	}
	return 0, fmt.Errorf("container cgroup peak file not found; mount host cgroup read-only")
}
func cgroupPeakFiles(raw, id string) []string {
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		path := strings.TrimPrefix(filepath.Clean("/"+parts[2]), "/")
		// Host PIDs can coincide with unrelated PIDs inside the judge's namespace.
		// Accept only a cgroup containing this container's ID, and read the container
		// root rather than an individual Sentry/Gofer descendant's peak.
		components := strings.Split(path, "/")
		matched := false
		for i, component := range components {
			if component == id || component == "docker-"+id+".scope" {
				path = strings.Join(components[:i+1], "/")
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			files = append(files, filepath.Join(path, "memory.peak"))
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller == "memory" {
				files = append(files, filepath.Join("memory", path, "memory.max_usage_in_bytes"))
			}
		}
	}
	return files
}
func readMemoryFileKB(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, fmt.Errorf("negative cgroup memory value")
	}
	return int(value/1024 + boolInt(value%1024 != 0)), nil
}
func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func buildContainerName(jobID string) string {
	suffix := strings.TrimSpace(jobID)

	if suffix == "" {
		suffix = strconv.FormatInt(time.Now().UnixNano(), 10)
	}

	suffix = sanitizeContainerNameSuffix(suffix)

	if len(suffix) > 24 {
		suffix = suffix[:24]
	}

	return "judge-" + suffix + "-" + rand.Text()[:10]
}

func sanitizeContainerNameSuffix(value string) string {
	var b strings.Builder

	for _, r := range value {
		if r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' ||
			r == '-' ||
			r == '_' {
			b.WriteRune(r)
		}
	}

	if b.Len() == 0 {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}

	return b.String()
}

func (e *RunscExecutor) forceRemoveContainer(containerName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.dockerBin, "rm", "-f", containerName)

	return cmd.Run()
}

func (e *RunscExecutor) forceKillContainer(containerName string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, e.dockerBin, "kill", containerName)

	return cmd.Run()
}
