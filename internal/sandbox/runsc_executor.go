package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
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

	args := []string{"run", "--rm", "--runtime=" + runtimeName}

	containerName := "judge-run"
	if req.JobID != "" {
		jobID := req.JobID
		if len(jobID) > 12 {
			jobID = jobID[:12]
		}
		containerName = "judge-" + jobID
	}
	args = append(args, "--name", containerName)

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
	if req.TmpfsSizeMB > 0 {
		args = append(args, "--tmpfs", fmt.Sprintf("/tmp:size=%dm", req.TmpfsSizeMB))
	}

	args = append(args,
		"--security-opt", "no-new-privileges",
		"-v", fmt.Sprintf("%s:/workspace:rw", filepath.Clean(req.WorkDirHost)),
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

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
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