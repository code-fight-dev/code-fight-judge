package sandbox

import (
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"code-fight-judge/internal/kube"
	"code-fight-judge/internal/supervisor"

	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const kubernetesRuntimeClass = "code-fight-gvisor"

type KubernetesExecutor struct {
	client         *kube.Client
	workspace, pvc string
}

func NewKubernetesExecutor(workspace, pvc string) (*KubernetesExecutor, error) {
	client, err := kube.NewInClusterClient()
	if err != nil {
		return nil, err
	}
	return &KubernetesExecutor{client: client, workspace: workspace, pvc: pvc}, nil
}

func (e *KubernetesExecutor) CheckReady(ctx context.Context) error {
	return e.client.Request(ctx, http.MethodGet, "/apis/node.k8s.io/v1/runtimeclasses/"+kubernetesRuntimeClass, nil, nil)
}

func (e *KubernetesExecutor) Execute(ctx context.Context, req ExecuteRequest) (*ExecuteResult, error) {
	if req.Image == "" || len(req.Command) == 0 || req.Timeout <= 0 || req.MemoryMB < 6 ||
		req.CPUCount <= 0 || math.IsNaN(req.CPUCount) || math.IsInf(req.CPUCount, 0) ||
		req.OutputKB <= 0 || req.OutputKB > 1024*1024 || req.PidsLimit <= 0 {
		return nil, fmt.Errorf("invalid Kubernetes sandbox execution request")
	}
	workspace, err := e.subPath(req.WorkDirHost)
	if err != nil {
		return nil, err
	}
	output, err := e.subPath(req.OutputDirHost)
	if err != nil {
		return nil, err
	}
	// The task can write only to /output and its temporary files. The supervisor
	// writes its report via a distinct root-only mount.
	execution, err := os.MkdirTemp(filepath.Dir(req.WorkDirHost), "sandbox-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(execution)
	if err := os.WriteFile(filepath.Join(execution, "input"), []byte(req.Stdin), 0444); err != nil {
		return nil, err
	}
	reportDir := filepath.Join(execution, "report")
	if err := os.Mkdir(reportDir, 0700); err != nil {
		return nil, err
	}
	executionPath, err := e.subPath(execution)
	if err != nil {
		return nil, err
	}
	name := "judge-sandbox-" + strings.ToLower(rand.Text())
	job := e.job(name, workspace, output, executionPath, req)
	jobsPath := "/apis/batch/v1/namespaces/" + e.client.Namespace + "/jobs"
	deletePolicy := meta.DeletePropagationBackground
	// The request can fail after the API server has created the job.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = e.client.Request(cleanup, http.MethodDelete, jobsPath+"/"+name, meta.DeleteOptions{PropagationPolicy: &deletePolicy}, nil)
	}()
	var created batch.Job
	if err := e.client.Request(ctx, http.MethodPost, jobsPath+"?fieldValidation=Strict", job, &created); err != nil {
		return nil, err
	}
	if created.Spec.Template.Spec.Resources == nil ||
		!created.Spec.Template.Spec.Resources.Limits.Cpu().Equal(*job.Spec.Template.Spec.Resources.Limits.Cpu()) ||
		!created.Spec.Template.Spec.Resources.Limits.Memory().Equal(*job.Spec.Template.Spec.Resources.Limits.Memory()) {
		return nil, fmt.Errorf("Kubernetes did not retain the sandbox pod resource limits")
	}

	deadline, cancel := context.WithTimeout(ctx, req.Timeout+45*time.Second)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		report, err := reportFromFile(filepath.Join(reportDir, supervisor.ResultFile), req.OutputKB*1024)
		if err == nil {
			return report, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		var podList core.PodList
		podsPath := "/api/v1/namespaces/" + e.client.Namespace + "/pods?labelSelector=batch.kubernetes.io%2Fjob-name%3D" + name
		if err := e.client.Request(deadline, http.MethodGet, podsPath, nil, &podList); err != nil {
			return nil, err
		}
		for _, pod := range podList.Items {
			for _, status := range pod.Status.ContainerStatuses {
				if terminal := status.State.Terminated; terminal != nil {
					// The trusted report is renamed before the supervisor exits.
					report, err := reportFromFile(filepath.Join(reportDir, supervisor.ResultFile), req.OutputKB*1024)
					if err == nil || !os.IsNotExist(err) {
						return report, err
					}
					if terminal.Reason == "OOMKilled" {
						return &ExecuteResult{ExitCode: int(terminal.ExitCode), OOMKilled: true, TimeUnavailable: true}, nil
					}
					return nil, fmt.Errorf("Kubernetes sandbox exited without a report (%s, exit %d)", terminal.Reason, terminal.ExitCode)
				}
			}
		}
		select {
		case <-deadline.Done():
			return nil, fmt.Errorf("Kubernetes sandbox exceeded infrastructure deadline: %w", deadline.Err())
		case <-ticker.C:
		}
	}
}

func (e *KubernetesExecutor) subPath(path string) (string, error) {
	relative, err := filepath.Rel(e.workspace, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("sandbox path is outside the shared workspace")
	}
	return filepath.ToSlash(relative), nil
}

func reportFromFile(path string, limit int) (*ExecuteResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result, err := decodeSupervisorReport(file, limit)
	if err != nil {
		return nil, err
	}
	if result.Error != "" {
		return nil, fmt.Errorf("sandbox supervisor: %s", result.Error)
	}
	return &ExecuteResult{
		ExitCode: result.ExitCode, Stdout: string(result.Stdout), Stderr: string(result.Stderr),
		DurationMS: result.DurationMS, TimedOut: result.TimedOut, OutputExceeded: result.OutputExceeded,
	}, nil
}

func (e *KubernetesExecutor) job(name, workspace, output, execution string, req ExecuteRequest) batch.Job {
	args := []string{"-timeout", req.Timeout.String(), "-output-bytes", strconv.Itoa(req.OutputKB * 1024),
		"-pids", strconv.Itoa(req.PidsLimit), "-exit-after-report", "--"}
	tmpfsMB := req.TmpfsSizeMB
	if tmpfsMB <= 0 {
		tmpfsMB = 64
	}
	limits := core.ResourceList{
		core.ResourceCPU:    *resource.NewMilliQuantity(int64(math.Ceil(req.CPUCount*1000)), resource.DecimalSI),
		core.ResourceMemory: resource.MustParse(strconv.Itoa(req.MemoryMB) + "Mi"),
	}
	env := []core.EnvVar{{Name: "TMPDIR", Value: "/tmp"}, {Name: "GOTMPDIR", Value: "/tmp"}, {Name: "GOCACHE", Value: "/gocache"}}
	keys := make([]string, 0, len(req.Env))
	for key := range req.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, core.EnvVar{Name: key, Value: req.Env[key]})
	}
	return batch.Job{
		ObjectMeta: meta.ObjectMeta{Name: name},
		Spec: batch.JobSpec{
			BackoffLimit: pointer(int32(0)), TTLSecondsAfterFinished: pointer(int32(60)),
			ActiveDeadlineSeconds: pointer(int64(math.Ceil(req.Timeout.Seconds())) + 60),
			Template: core.PodTemplateSpec{
				ObjectMeta: meta.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/component": "judge-sandbox"}},
				Spec: core.PodSpec{
					RuntimeClassName: pointer(kubernetesRuntimeClass), ServiceAccountName: "default",
					// gVisor shares the Sentry across the pod. The kubelet must apply
					// the limits to the parent pod cgroup as well as the container.
					Resources:                    &core.ResourceRequirements{Limits: limits},
					AutomountServiceAccountToken: pointer(false), EnableServiceLinks: pointer(false),
					RestartPolicy: core.RestartPolicyNever, TerminationGracePeriodSeconds: pointer(int64(1)),
					Containers: []core.Container{{
						Name: "sandbox", Image: req.Image, ImagePullPolicy: core.PullNever,
						Command: []string{"/usr/local/bin/judge-supervisor"}, Args: append(args, req.Command...),
						WorkingDir: "/workspace", Env: env, Resources: core.ResourceRequirements{Limits: limits},
						SecurityContext: &core.SecurityContext{
							RunAsUser: pointer(int64(0)), RunAsGroup: pointer(int64(0)),
							AllowPrivilegeEscalation: pointer(false), ReadOnlyRootFilesystem: pointer(true),
							Capabilities:   &core.Capabilities{Drop: []core.Capability{"ALL"}, Add: []core.Capability{"SETUID", "SETGID", "KILL"}},
							SeccompProfile: &core.SeccompProfile{Type: core.SeccompProfileTypeRuntimeDefault},
						},
						VolumeMounts: []core.VolumeMount{
							{Name: "job-data", MountPath: "/workspace", SubPath: workspace, ReadOnly: true},
							{Name: "job-data", MountPath: "/output", SubPath: output, ReadOnly: req.OutputDirReadOnly},
							{Name: "job-data", MountPath: "/judge-stdin", SubPath: execution + "/input", ReadOnly: true},
							{Name: "job-data", MountPath: "/judge-report", SubPath: execution + "/report"},
							{Name: "tmp", MountPath: "/tmp"}, {Name: "gocache", MountPath: "/gocache"},
						},
					}},
					Volumes: []core.Volume{
						{Name: "job-data", VolumeSource: core.VolumeSource{PersistentVolumeClaim: &core.PersistentVolumeClaimVolumeSource{ClaimName: e.pvc}}},
						memoryVolume("tmp", strconv.Itoa(tmpfsMB)+"Mi"), memoryVolume("gocache", "256Mi"),
					},
				},
			},
		},
	}
}

func memoryVolume(name, size string) core.Volume {
	return core.Volume{Name: name, VolumeSource: core.VolumeSource{EmptyDir: &core.EmptyDirVolumeSource{
		Medium: core.StorageMediumMemory, SizeLimit: pointer(resource.MustParse(size)),
	}}}
}

func pointer[T any](value T) *T { return &value }
