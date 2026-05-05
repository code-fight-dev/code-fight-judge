package sandbox

import (
	"context"
	"time"
)

type Executor interface {
	Execute(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error)
}

type ExecuteRequest struct {
	JobID             string
	Image             string
	WorkDirHost       string
	OutputDirHost     string
	OutputDirReadOnly bool
	Command           []string
	Env               map[string]string
	Stdin             string

	CPUCount     float64
	MemoryMB     int
	PidsLimit    int
	ReadOnlyRoot bool
	NetworkNone  bool
	TmpfsSizeMB  int
	RuntimeName  string
	Timeout      time.Duration
}

type ExecuteResult struct {
	ExitCode     int
	Stdout       string
	Stderr       string
	DurationMS   int64
	PeakMemoryKB int
	TimedOut     bool
	OOMKilled    bool
}
