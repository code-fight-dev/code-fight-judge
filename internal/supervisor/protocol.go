// Package supervisor defines the trusted execution report shared by the
// container supervisor and the judge. Reports never travel through user stdout.
package supervisor

const ResultFile = "result.json"
const ProtocolVersion = 1

type Result struct {
	Version        int    `json:"version"`
	ExitCode       int    `json:"exit_code"`
	Stdout         []byte `json:"stdout"`
	Stderr         []byte `json:"stderr"`
	DurationMS     int64  `json:"duration_ms"`
	TimedOut       bool   `json:"timed_out"`
	OutputExceeded bool   `json:"output_exceeded"`
	Error          string `json:"error,omitempty"`
}
