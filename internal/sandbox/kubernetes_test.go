package sandbox

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSandboxOOMUsesOnlyItsPodCgroup(t *testing.T) {
	root := t.TempDir()
	pod := core.Pod{
		ObjectMeta: meta.ObjectMeta{Name: "sandbox", UID: "ca21a2c4-144c-4951-8db3-92de63c93d44"},
		Spec:       core.PodSpec{NodeName: "node-1"},
	}
	executor := &KubernetesExecutor{nodeName: "node-1", cgroupRoot: root}
	// A node-level OOM or a memory event from another pod is irrelevant.
	if err := os.WriteFile(filepath.Join(root, "memory.events"), []byte("oom 3\noom_kill 3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if killed, err := executor.sandboxOOM(pod); err == nil || killed {
		t.Fatalf("missing pod cgroup accepted: killed=%v err=%v", killed, err)
	}
	group := filepath.Join(root, "kubepods", "pod"+string(pod.UID))
	if err := os.MkdirAll(group, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		events string
		killed bool
	}{
		{"ordinary SIGKILL", "oom 0\noom_kill 0\n", false},
		{"global OOM victim", "oom 0\noom_kill 1\n", false},
		{"reached limit without kill", "oom 1\noom_kill 0\n", false},
		{"sandbox memory limit", "oom 1\noom_kill 1\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(group, "memory.events"), []byte(tc.events), 0600); err != nil {
				t.Fatal(err)
			}
			killed, err := executor.sandboxOOM(pod)
			if err != nil || killed != tc.killed {
				t.Fatalf("sandbox OOM = %v, %v; want %v", killed, err, tc.killed)
			}
		})
	}
}

func TestKubernetesSandboxJobUsesBoundedIsolatedRuntime(t *testing.T) {
	executor := &KubernetesExecutor{pvc: "judge-workspace"}
	request := ExecuteRequest{
		Image: "judge-python:3.12", Command: []string{"python3", "/workspace/main.py"},
		CPUCount: 0.25, MemoryMB: 64, PidsLimit: 32, Timeout: 1500 * time.Millisecond, OutputKB: 8,
		OutputDirReadOnly: true,
	}
	job := executor.job("judge-sandbox-unit", "judge-job-1/workspace", "judge-job-1/output", "judge-job-1/sandbox-1", request)
	pod := job.Spec.Template.Spec
	if *pod.RuntimeClassName != kubernetesRuntimeClass || *pod.AutomountServiceAccountToken || pod.HostNetwork ||
		*job.Spec.BackoffLimit != 0 || *job.Spec.ActiveDeadlineSeconds != 62 || job.Spec.TTLSecondsAfterFinished == nil {
		t.Fatalf("unexpected Kubernetes sandbox lifecycle: %#v", job.Spec)
	}
	container := pod.Containers[0]
	if container.Resources.Limits.Cpu().MilliValue() != 250 || container.Resources.Limits.Memory().Value() != 64<<20 {
		t.Fatalf("unexpected limits: %v", container.Resources.Limits)
	}
	if pod.Resources == nil || !pod.Resources.Limits.Cpu().Equal(*container.Resources.Limits.Cpu()) ||
		!pod.Resources.Limits.Memory().Equal(*container.Resources.Limits.Memory()) {
		t.Fatalf("pod resources must bound the shared Sentry: %v", pod.Resources)
	}
	if container.ImagePullPolicy != core.PullNever || *container.SecurityContext.AllowPrivilegeEscalation ||
		!*container.SecurityContext.ReadOnlyRootFilesystem || container.Command[0] != "/usr/local/bin/judge-supervisor" {
		t.Fatalf("unexpected sandbox container: %#v", container)
	}
	for _, mount := range container.VolumeMounts {
		if mount.Name == "job-data" && (mount.SubPath == "" || (mount.MountPath != "/judge-report" && !mount.ReadOnly)) {
			t.Fatalf("unsafe runtime mount: %#v", mount)
		}
	}
}

func TestKubernetesSandboxWorkspaceRejectsSiblingPaths(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "jobs")
	executor := &KubernetesExecutor{workspace: workspace}
	if path, err := executor.subPath(filepath.Join(workspace, "judge-job-1", "workspace")); err != nil || path != "judge-job-1/workspace" {
		t.Fatalf("relative workspace = %q, %v", path, err)
	}
	if _, err := executor.subPath(filepath.Join(base, "jobs-other", "workspace")); err == nil {
		t.Fatal("outside workspace path accepted")
	}
}
