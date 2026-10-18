package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code-fight-judge/internal/supervisor"
)

func testRequest(t *testing.T) ExecuteRequest {
	t.Helper()
	return ExecuteRequest{JobID: "job", Image: "runtime:test", WorkDirHost: t.TempDir(), OutputDirHost: t.TempDir(), Command: []string{"run"}, CPUCount: 1, MemoryMB: 128, PidsLimit: 64, Timeout: time.Second, OutputKB: 1, NetworkNone: true, ReadOnlyRoot: true, OutputDirReadOnly: true}
}

func TestExecuteValidatesRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ExecuteRequest)
		message string
	}{
		{"image", func(r *ExecuteRequest) { r.Image = "" }, "image is required"},
		{"work", func(r *ExecuteRequest) { r.WorkDirHost = "" }, "work_dir_host is required"},
		{"output", func(r *ExecuteRequest) { r.OutputDirHost = "" }, "output_dir_host is required"},
		{"command", func(r *ExecuteRequest) { r.Command = nil }, "command is required"},
		{"timeout", func(r *ExecuteRequest) { r.Timeout = 0 }, "positive timeout"},
		{"output limit", func(r *ExecuteRequest) { r.OutputKB = 0 }, "positive timeout"},
		{"memory", func(r *ExecuteRequest) { r.MemoryMB = 5 }, "memory >= 6"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := testRequest(t)
			tt.mutate(&req)
			_, err := NewRunscExecutor().Execute(context.Background(), req)
			if err == nil || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestExecuteReadsTrustedReportBeforeCleanup(t *testing.T) {
	bin, root := buildFakeDocker(t)
	t.Setenv("FAKE_DOCKER_STDOUT", "ok")
	t.Setenv("FAKE_DOCKER_EXIT", "124")
	cgroupRoot := t.TempDir()
	peakPath := filepath.Join(cgroupRoot, "docker", "container-id", "memory.peak")
	if err := os.MkdirAll(filepath.Dir(peakPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peakPath, []byte("2097152"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_DOCKER_PEAK", peakPath)
	e := &RunscExecutor{dockerBin: bin, cgroupRoots: []string{cgroupRoot}, procRoots: []string{t.TempDir()}}
	req := testRequest(t)
	req.Stdin = "input"
	result, err := e.Execute(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 124 || result.TimedOut || result.OOMKilled || result.Stdout != "ok" || result.DurationMS != 7 || result.PeakMemoryKB != 2048 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if _, err := os.Stat(peakPath); !os.IsNotExist(err) {
		t.Fatalf("cleanup did not remove fake cgroup: %v", err)
	}
	args, err := os.ReadFile(filepath.Join(root, "args"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--memory-swap\n128m", "--user\n0:0", "--entrypoint\n/usr/local/bin/judge-supervisor", "-timeout\n1s", "-output-bytes\n1024", ":/output:ro", "--log-driver\nnone"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("missing %q in arguments %s", want, args)
		}
	}
}

func TestExecuteHonorsTrustedStopReasons(t *testing.T) {
	for _, flag := range []string{"FAKE_DOCKER_TIMEOUT", "FAKE_DOCKER_OUTPUT_EXCEEDED"} {
		t.Run(flag, func(t *testing.T) {
			bin, _ := buildFakeDocker(t)
			t.Setenv(flag, "1")
			result, err := (&RunscExecutor{dockerBin: bin}).Execute(context.Background(), testRequest(t))
			if err != nil {
				t.Fatal(err)
			}
			if flag == "FAKE_DOCKER_TIMEOUT" && !result.TimedOut || flag == "FAKE_DOCKER_OUTPUT_EXCEEDED" && !result.OutputExceeded {
				t.Fatalf("result=%#v", result)
			}
		})
	}
}

func TestExecuteClassifiesOnlyConfirmedOOM(t *testing.T) {
	for _, oom := range []string{"false", "true"} {
		t.Run(oom, func(t *testing.T) {
			bin, _ := buildFakeDocker(t)
			t.Setenv("FAKE_DOCKER_DEAD", "1")
			t.Setenv("FAKE_DOCKER_NO_REPORT", "1")
			t.Setenv("FAKE_DOCKER_EXIT", "137")
			t.Setenv("FAKE_DOCKER_OOM", oom)
			result, err := (&RunscExecutor{dockerBin: bin}).Execute(context.Background(), testRequest(t))
			if oom == "true" {
				if err != nil || !result.OOMKilled || !result.TimeUnavailable {
					t.Fatalf("result=%#v err=%v", result, err)
				}
			} else if err == nil {
				t.Fatal("unconfirmed exit 137 classified as OOM")
			}
		})
	}
}

func TestExecuteReturnsCancellationAndCleansUp(t *testing.T) {
	bin, root := buildFakeDocker(t)
	t.Setenv("FAKE_DOCKER_NO_REPORT", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := (&RunscExecutor{dockerBin: bin}).Execute(ctx, testRequest(t))
	if err == nil {
		t.Fatal("expected cancellation")
	}
	if _, err := os.Stat(filepath.Join(root, "removed")); err != nil {
		t.Fatal("container was not removed")
	}
}

func TestStartFailureIsNotIgnored(t *testing.T) {
	bin, _ := buildFakeDocker(t)
	t.Setenv("FAKE_DOCKER_START_FAIL", "1")
	_, err := (&RunscExecutor{dockerBin: bin}).Execute(context.Background(), testRequest(t))
	if err == nil || !strings.Contains(err.Error(), "docker start failed") {
		t.Fatalf("error=%v", err)
	}
}

func TestDockerDiagnosticsAreBounded(t *testing.T) {
	bin, _ := buildFakeDocker(t)
	t.Setenv("FAKE_DOCKER_CREATE_FAIL", "1")
	_, err := (&RunscExecutor{dockerBin: bin}).Execute(context.Background(), testRequest(t))
	if err == nil || len(err.Error()) > 17000 {
		t.Fatalf("unbounded or missing diagnostic error (size=%d)", len(err.Error()))
	}
}

func TestReadReportRejectsExcessOutputAndPreservesBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.json")
	raw, _ := json.Marshal(supervisor.Result{Version: supervisor.ProtocolVersion, Stdout: []byte{0, 255}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	report, err := decodeSupervisorReport(strings.NewReader(string(raw)), 2)
	if err != nil || string(report.Stdout) != string([]byte{0, 255}) {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	if _, err := decodeSupervisorReport(strings.NewReader(string(raw)), 1); err == nil {
		t.Fatal("excess output allowed")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 5000)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSupervisorReport(strings.NewReader(strings.Repeat("x", 5000)), 1); err == nil {
		t.Fatal("oversize report allowed")
	}
	if _, err := decodeSupervisorReport(strings.NewReader("{}"), 1024); err == nil {
		t.Fatal("unversioned report accepted")
	}
}

func TestCgroupPeakPathsAndUnknownMemory(t *testing.T) {
	paths := cgroupPeakFiles("0::/custom/docker-id.scope/child\n7:memory:/docker/id/child", "id")
	if len(paths) != 2 || paths[0] != "custom/docker-id.scope/memory.peak" || paths[1] != "memory/docker/id/memory.max_usage_in_bytes" {
		t.Fatalf("paths=%v", paths)
	}
	if files := cgroupPeakFiles("0::/\n0::/unrelated.scope", "id"); len(files) != 0 {
		t.Fatalf("unrelated cgroup accepted: %v", files)
	}
	bin, _ := buildFakeDocker(t)
	root := t.TempDir()
	current := filepath.Join(root, "docker", "container-id", "memory.current")
	os.MkdirAll(filepath.Dir(current), 0755)
	os.WriteFile(current, []byte("1024"), 0644)
	e := &RunscExecutor{dockerBin: bin, cgroupRoots: []string{root}, procRoots: []string{t.TempDir()}}
	if _, err := e.readCgroupPeakMemoryKB("container-id"); err == nil {
		t.Fatal("current memory treated as lifetime peak")
	}
}

func TestReadMemoryFileRoundsUpAndRejectsNegative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.peak")
	for _, tt := range []struct {
		raw   string
		want  int
		valid bool
	}{{"0", 0, true}, {"1", 1, true}, {"1025", 2, true}, {"-1", 0, false}, {"max", 0, false}, {"", 0, false}} {
		os.WriteFile(path, []byte(tt.raw), 0644)
		got, err := readMemoryFileKB(path)
		if (err == nil) != tt.valid || tt.valid && got != tt.want {
			t.Fatalf("raw=%q got=%d err=%v", tt.raw, got, err)
		}
	}
}

func TestContainerNamesAreUniqueAcrossAttempts(t *testing.T) {
	first, second := buildContainerName("job-id-compile"), buildContainerName("job-id-compile")
	if first == second || !strings.HasPrefix(first, "judge-job-id-compile-") {
		t.Fatalf("names=%s/%s", first, second)
	}
	if got := sanitizeContainerNameSuffix("a/b c!_-"); got != "abc_-" {
		t.Fatalf("suffix=%s", got)
	}
}

func buildFakeDocker(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FAKE_DOCKER_ROOT", root)
	source := filepath.Join(root, "docker.go")
	bin := filepath.Join(root, "docker")
	code := `package main
import("encoding/json";"fmt";"io";"os";"path/filepath";"strings";"strconv")
func main(){
 args:=os.Args[1:];root:=os.Getenv("FAKE_DOCKER_ROOT")
 if len(args)==0{return}
 switch args[0]{
 case "create":
  if os.Getenv("FAKE_DOCKER_CREATE_FAIL")!=""{fmt.Fprint(os.Stderr,strings.Repeat("x",100000));os.Exit(1)}
  os.WriteFile(filepath.Join(root,"args"),[]byte(strings.Join(args,"\n")),0600)

  fmt.Println("container-id")
 case "start":
  if os.Getenv("FAKE_DOCKER_START_FAIL")!=""{fmt.Fprint(os.Stderr,"start failed");os.Exit(1)}
  io.Copy(io.Discard,os.Stdin)
  if os.Getenv("FAKE_DOCKER_NO_REPORT")!=""{return}
  dir:=[]byte(root)
  exit,_:=strconv.Atoi(os.Getenv("FAKE_DOCKER_EXIT"))
  result:=struct{ExitCode int;Stdout,Stderr []byte;DurationMS int64;TimedOut,OutputExceeded bool}{exit,[]byte(os.Getenv("FAKE_DOCKER_STDOUT")),[]byte(os.Getenv("FAKE_DOCKER_STDERR")),7,os.Getenv("FAKE_DOCKER_TIMEOUT")!="",os.Getenv("FAKE_DOCKER_OUTPUT_EXCEEDED")!=""}
  // Match the supervisor's snake_case wire fields.
  raw,_:=json.Marshal(map[string]any{"version":1,"exit_code":result.ExitCode,"stdout":result.Stdout,"stderr":result.Stderr,"duration_ms":result.DurationMS,"timed_out":result.TimedOut,"output_exceeded":result.OutputExceeded})
  os.WriteFile(filepath.Join(string(dir),"result.tmp"),raw,0644);os.Rename(filepath.Join(string(dir),"result.tmp"),filepath.Join(string(dir),"result.json"))
 case "exec":raw,err:=os.ReadFile(filepath.Join(root,"result.json"));if err!=nil{os.Exit(1)};os.Stdout.Write(raw)
 case "inspect":
  if os.Getenv("FAKE_DOCKER_INSPECT_FAIL")!=""{os.Exit(1)}
  format:=args[2]
  switch format{
  case "{{.State.OOMKilled}}":oom:=os.Getenv("FAKE_DOCKER_OOM");if oom==""{oom="false"};fmt.Println(oom)
  case "{{.State.Pid}}":fmt.Println("0")
  default:status:="running";if os.Getenv("FAKE_DOCKER_DEAD")!=""{status="exited"};exit,_:=strconv.Atoi(os.Getenv("FAKE_DOCKER_EXIT"));raw,_:=json.Marshal(map[string]any{"Status":status,"ExitCode":exit});fmt.Println(string(raw))
  }
 case "rm":os.WriteFile(filepath.Join(root,"removed"),[]byte("yes"),0600);if path:=os.Getenv("FAKE_DOCKER_PEAK");path!=""{os.Remove(path)}
 case "kill":
 }
}`
	if err := os.WriteFile(source, []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("go", "build", "-o", bin, source).CombinedOutput(); err != nil {
		t.Fatalf("fake docker: %v %s", err, output)
	}
	return bin, root
}
