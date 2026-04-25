package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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
	if len(req.Command) == 0 {
		return nil, fmt.Errorf("sandbox execute: command is required")
	}

	runtimeName := req.RuntimeName
	if runtimeName == "" {
		runtimeName = "runsc"
	}

	containerName := buildContainerName(req.JobID)

	args := []string{
		"run",
		"--rm",
		"--runtime=" + runtimeName,
		"--name", containerName,
		"--security-opt", "no-new-privileges",
	}

	if req.NetworkNone {
		args = append(args, "--network", "none")
	}

	if req.CPUCount > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(req.CPUCount, 'f', -1, 64))
	}

	if req.MemoryMB > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", req.MemoryMB))
	}

	if req.PidsLimit > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(req.PidsLimit))
	}

	if req.ReadOnlyRoot {
		args = append(args, "--read-only")
	}

	tmpfsSizeMB := req.TmpfsSizeMB
	if tmpfsSizeMB <= 0 {
		tmpfsSizeMB = 256
	}

	args = append(args,
		"--tmpfs", fmt.Sprintf("/tmp:exec,size=%dm", tmpfsSizeMB),
		"--tmpfs", "/gocache:exec,size=256m",
		"-e", "TMPDIR=/tmp",
		"-e", "GOTMPDIR=/tmp",
		"-e", "GOCACHE=/gocache",
	)

	args = append(args,
		"-v", fmt.Sprintf("%s:/workspace:ro", filepath.Clean(req.WorkDirHost)),
		"-w", "/workspace",
	)

	for k, v := range req.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}

	args = append(args, req.Image)
	args = append(args, req.Command...)

	started := time.Now()

	cmd := exec.CommandContext(ctx, e.dockerBin, args...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(started).Milliseconds()

	if ctx.Err() != nil {
		_ = e.forceRemoveContainer(containerName)

		return &ExecuteResult{
			ExitCode:   -1,
			Stdout:     stdout.String(),
			Stderr:     stderr.String(),
			DurationMS: duration,
		}, nil
	}

	exitCode := 0

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			_ = e.forceRemoveContainer(containerName)

			return nil, fmt.Errorf(
				"sandbox execute: docker run failed: %w; args=%v; stderr=%s",
				err,
				args,
				stderr.String(),
			)
		}
	}

	return &ExecuteResult{
		ExitCode:   exitCode,
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		DurationMS: duration,
	}, nil
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