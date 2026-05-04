package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type RunscExecutor struct {
	dockerBin string
}

func NewRunscExecutor() *RunscExecutor {
	return &RunscExecutor{dockerBin: "docker"}
}

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

	runtimeName := req.RuntimeName
	if runtimeName == "" {
		runtimeName = "runsc"
	}

	containerName := buildContainerName(req.JobID)

	createArgs := []string{
		"create",
		"--runtime=" + runtimeName,
		"--name", containerName,
		"--security-opt", "no-new-privileges",
	}

	if req.Stdin != "" {
		createArgs = append(createArgs, "-i")
	}

	if req.NetworkNone {
		createArgs = append(createArgs, "--network", "none")
	}

	if req.CPUCount > 0 {
		createArgs = append(createArgs, "--cpus", strconv.FormatFloat(req.CPUCount, 'f', -1, 64))
	}

	if req.MemoryMB > 0 {
		createArgs = append(createArgs, "--memory", fmt.Sprintf("%dm", req.MemoryMB))
	}

	if req.PidsLimit > 0 {
		createArgs = append(createArgs, "--pids-limit", strconv.Itoa(req.PidsLimit))
	}

	if req.ReadOnlyRoot {
		createArgs = append(createArgs, "--read-only")
	}

	tmpfsSizeMB := req.TmpfsSizeMB
	if tmpfsSizeMB <= 0 {
		tmpfsSizeMB = 256
	}

	createArgs = append(createArgs,
		"--tmpfs", fmt.Sprintf("/tmp:exec,size=%dm", tmpfsSizeMB),
		"--tmpfs", "/gocache:exec,size=256m",
		"-e", "TMPDIR=/tmp",
		"-e", "GOTMPDIR=/tmp",
		"-e", "GOCACHE=/gocache",
	)

	createArgs = append(createArgs,
		"-v", fmt.Sprintf("%s:/workspace:ro", filepath.Clean(req.WorkDirHost)),
		"-v", fmt.Sprintf("%s:/output:rw", filepath.Clean(req.OutputDirHost)),
		"-w", "/workspace",
	)

	for k, v := range req.Env {
		createArgs = append(createArgs, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	createArgs = append(createArgs, req.Image)
	createArgs = append(createArgs, req.Command...)

	started := time.Now()

	containerID, err := e.createContainer(ctx, createArgs)
	if err != nil {
		return nil, err
	}
	log.Printf("sandbox execute: container created name=%s id=%s timeout=%s", containerName, containerID, req.Timeout)
	defer e.forceRemoveContainer(containerName)

	var runCtx context.Context
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}

	forceDone := make(chan struct{})
	stopForce := make(chan struct{})
	go func() {
		defer close(forceDone)

		select {
		case <-runCtx.Done():
			_ = e.forceKillContainer(containerName)
		case <-stopForce:
		}
	}()
	defer func() {
		if runCtx.Err() == nil {
			close(stopForce)
		}
		cancel()
		<-forceDone
	}()

	monitor := e.startStatsMonitor(runCtx, containerID)

	if err := e.startContainer(runCtx, containerID, req.Stdin); err != nil {
		duration := time.Since(started).Milliseconds()
		if runCtx.Err() != nil {
			<-forceDone
		}
		stdout, stderr := e.readContainerLogs(context.Background(), containerID)
		peakMemoryKB := e.maxObservedMemoryKB(containerID, monitor.Stop())
		log.Printf(
			"sandbox execute: container finished after start error name=%s id=%s duration_ms=%d peak_memory_kb=%d err=%v ctx_err=%v",
			containerName,
			containerID,
			duration,
			peakMemoryKB,
			err,
			runCtx.Err(),
		)

		if runCtx.Err() != nil {
			return &ExecuteResult{
				ExitCode:     -1,
				Stdout:       stdout,
				Stderr:       stderr,
				DurationMS:   duration,
				PeakMemoryKB: peakMemoryKB,
				TimedOut:     errors.Is(runCtx.Err(), context.DeadlineExceeded),
			}, nil
		}

		return nil, err
	}

	exitCode, err := e.waitContainer(runCtx, containerID)
	duration := time.Since(started).Milliseconds()
	if err != nil && runCtx.Err() != nil {
		<-forceDone
	}
	stdout, stderr := e.readContainerLogs(context.Background(), containerID)
	peakMemoryKB := e.maxObservedMemoryKB(containerID, monitor.Stop())
	log.Printf(
		"sandbox execute: container finished name=%s id=%s exit_code=%d duration_ms=%d peak_memory_kb=%d wait_err=%v ctx_err=%v",
		containerName,
		containerID,
		exitCode,
		duration,
		peakMemoryKB,
		err,
		runCtx.Err(),
	)

	if err != nil {
		if runCtx.Err() != nil {
			return &ExecuteResult{
				ExitCode:     -1,
				Stdout:       stdout,
				Stderr:       stderr,
				DurationMS:   duration,
				PeakMemoryKB: peakMemoryKB,
				TimedOut:     errors.Is(runCtx.Err(), context.DeadlineExceeded),
			}, nil
		}

		return nil, err
	}

	return &ExecuteResult{
		ExitCode:     exitCode,
		Stdout:       stdout,
		Stderr:       stderr,
		DurationMS:   duration,
		PeakMemoryKB: peakMemoryKB,
	}, nil
}

func (e *RunscExecutor) createContainer(ctx context.Context, args []string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, e.dockerBin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"sandbox execute: docker create failed: %w; args=%v; stderr=%s",
			err,
			args,
			stderr.String(),
		)
	}

	containerID := strings.TrimSpace(stdout.String())
	if containerID == "" {
		return "", fmt.Errorf("sandbox execute: docker create returned empty container id")
	}

	return containerID, nil
}

func (e *RunscExecutor) startContainer(ctx context.Context, containerID string, stdin string) error {
	args := []string{"start"}

	if stdin != "" {
		args = append(args, "--attach", "--interactive")
	}

	args = append(args, containerID)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.dockerBin, args...)
	cmd.Stderr = &stderr

	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}

	if err := cmd.Run(); err != nil {
		if isExitError(err) {
			return nil
		}

		return fmt.Errorf(
			"sandbox execute: docker start failed: %w; args=%v; stderr=%s",
			err,
			args,
			stderr.String(),
		)
	}

	return nil
}

func (e *RunscExecutor) waitContainer(ctx context.Context, containerID string) (int, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	args := []string{"wait", containerID}
	cmd := exec.CommandContext(ctx, e.dockerBin, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return -1, fmt.Errorf(
			"sandbox execute: docker wait failed: %w; args=%v; stderr=%s",
			err,
			args,
			stderr.String(),
		)
	}

	exitCodeText := strings.TrimSpace(stdout.String())
	exitCode, err := strconv.Atoi(exitCodeText)
	if err != nil {
		return -1, fmt.Errorf("sandbox execute: parse docker wait exit code %q: %w", exitCodeText, err)
	}

	return exitCode, nil
}

func (e *RunscExecutor) readContainerLogs(ctx context.Context, containerID string) (string, string) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := exec.CommandContext(ctx, e.dockerBin, "logs", containerID)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	_ = cmd.Run()

	return stdout.String(), stderr.String()
}

type statsMonitor struct {
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	peakKB int
}

func (e *RunscExecutor) startStatsMonitor(ctx context.Context, containerID string) *statsMonitor {
	monitorCtx, cancel := context.WithCancel(ctx)
	monitor := &statsMonitor{
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go func() {
		defer close(monitor.done)
		log.Printf("sandbox stats: monitor started container_id=%s", containerID)
		defer func() {
			monitor.mu.Lock()
			peakKB := monitor.peakKB
			monitor.mu.Unlock()
			log.Printf("sandbox stats: monitor stopped container_id=%s peak_memory_kb=%d", containerID, peakKB)
		}()

		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			sampled := monitor.sample(monitorCtx, e, containerID)

			select {
			case <-monitorCtx.Done():
				return
			case <-ticker.C:
			}

			if !sampled {
				continue
			}
		}
	}()

	return monitor
}

func (m *statsMonitor) Stop() int {
	m.cancel()
	<-m.done

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.peakKB
}

func (m *statsMonitor) sample(ctx context.Context, e *RunscExecutor, containerID string) bool {
	raw, err := e.readMemoryUsage(ctx, containerID)
	if err != nil {
		log.Printf("sandbox stats: sample error container_id=%s err=%v", containerID, err)
		return false
	}

	memoryKB, err := parseDockerMemoryUsageKB(raw)
	if err != nil {
		log.Printf("sandbox stats: parse memory usage error raw=%q err=%v", raw, err)
		return false
	}
	if memoryKB <= 0 {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if memoryKB > m.peakKB {
		m.peakKB = memoryKB
		log.Printf("sandbox stats: new peak memory sample peak_memory_kb=%d", memoryKB)
	}

	return true
}

func (e *RunscExecutor) readMemoryUsage(ctx context.Context, containerID string) (string, error) {
	statsCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd := exec.CommandContext(
		statsCtx,
		e.dockerBin,
		"stats",
		"--no-stream",
		"--format", "{{.MemUsage}}",
		containerID,
	)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w; stderr=%s", err, stderr.String())
	}

	return stdout.String(), nil
}

func (e *RunscExecutor) maxObservedMemoryKB(containerID string, statsPeakKB int) int {
	cgroupPeakKB, err := e.readCgroupPeakMemoryKB(containerID)
	if err != nil {
		log.Printf("sandbox memory: cgroup fallback unavailable container_id=%s stats_peak_kb=%d err=%v", containerID, statsPeakKB, err)
		return statsPeakKB
	}
	log.Printf("sandbox memory: cgroup fallback container_id=%s stats_peak_kb=%d cgroup_peak_kb=%d", containerID, statsPeakKB, cgroupPeakKB)

	if cgroupPeakKB > statsPeakKB {
		return cgroupPeakKB
	}

	return statsPeakKB
}

func (e *RunscExecutor) readCgroupPeakMemoryKB(containerID string) (int, error) {
	fullID := strings.TrimSpace(containerID)
	if fullID == "" {
		return 0, fmt.Errorf("empty container id")
	}

	roots := []string{"/host/sys/fs/cgroup", "/sys/fs/cgroup"}
	files := []string{
		filepath.Join("system.slice", "docker-"+fullID+".scope", "memory.peak"),
		filepath.Join("docker", fullID, "memory.peak"),
		filepath.Join("memory", "system.slice", "docker-"+fullID+".scope", "memory.max_usage_in_bytes"),
		filepath.Join("memory", "docker", fullID, "memory.max_usage_in_bytes"),
		filepath.Join("system.slice", "docker-"+fullID+".scope", "memory.current"),
		filepath.Join("docker", fullID, "memory.current"),
	}

	for _, root := range roots {
		for _, file := range files {
			value, err := readMemoryFileKB(filepath.Join(root, file))
			if err == nil && value > 0 {
				log.Printf("sandbox memory: read cgroup memory path=%s value_kb=%d", filepath.Join(root, file), value)
				return value, nil
			}
		}
	}

	return 0, fmt.Errorf("container cgroup memory file not found")
}

func readMemoryFileKB(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	value := strings.TrimSpace(string(raw))
	if value == "" || value == "max" {
		return 0, fmt.Errorf("invalid memory value %q", value)
	}

	bytesValue, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, err
	}

	return int((bytesValue + 1023) / 1024), nil
}

func parseDockerMemoryUsageKB(value string) (int, error) {
	used := strings.TrimSpace(stripANSIEscapeSequences(value))
	if idx := strings.Index(used, "/"); idx >= 0 {
		used = strings.TrimSpace(used[:idx])
	}

	if used == "" {
		return 0, fmt.Errorf("empty docker memory usage")
	}

	return parseDockerBytesToKB(used)
}

func stripANSIEscapeSequences(value string) string {
	var b strings.Builder

	for i := 0; i < len(value); i++ {
		if value[i] == 0x1b && i+1 < len(value) && value[i+1] == '[' {
			i += 2
			for i < len(value) {
				ch := value[i]
				if ch >= '@' && ch <= '~' {
					break
				}
				i++
			}
			continue
		}

		if value[i] < 0x20 && value[i] != '\t' && value[i] != '\n' && value[i] != '\r' {
			continue
		}

		b.WriteByte(value[i])
	}

	return b.String()
}

func parseDockerBytesToKB(value string) (int, error) {
	value = strings.TrimSpace(value)

	unitStart := len(value)
	for i, r := range value {
		if (r < '0' || r > '9') && r != '.' {
			unitStart = i
			break
		}
	}

	numberText := strings.TrimSpace(value[:unitStart])
	unit := strings.ToLower(strings.TrimSpace(value[unitStart:]))

	number, err := strconv.ParseFloat(numberText, 64)
	if err != nil {
		return 0, err
	}

	multiplier := float64(1)
	switch unit {
	case "b", "":
		multiplier = 1
	case "kb", "kib":
		multiplier = 1024
	case "mb", "mib":
		multiplier = 1024 * 1024
	case "gb", "gib":
		multiplier = 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown docker memory unit %q", unit)
	}

	return int((number*multiplier + 1023) / 1024), nil
}

func isExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
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

	return "judge-" + suffix
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
