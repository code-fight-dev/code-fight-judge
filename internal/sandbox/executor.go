package sandbox

import "context"

type Executor interface {
	Execute(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error)
}

type ExecuteRequest struct {
	JobID         string
	Image         string
	WorkDirHost   string
	OutputDirHost string
	Command       []string
	Env           map[string]string
	Stdin         string

	CPUCount     float64
	MemoryMB     int
	PidsLimit    int
	ReadOnlyRoot bool
	NetworkNone  bool
	TmpfsSizeMB  int
	RuntimeName  string
}

type ExecuteResult struct {
	ExitCode   int
	Stdout     string
	Stderr     string
	DurationMS int64
}