package sandbox

import (
	"path/filepath"
	"testing"
	"time"

	core "k8s.io/api/core/v1"
)

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
