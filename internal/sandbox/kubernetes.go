package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"code-fight-judge/internal/supervisor"

	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const kubernetesRuntimeClass = "code-fight-gvisor"
const credentialsPath = "/var/run/secrets/kubernetes.io/serviceaccount"

type KubernetesExecutor struct {
	client                    *http.Client
	namespace, workspace, pvc string
	nodeName, cgroupRoot      string
}

func NewKubernetesExecutor(workspace, pvc string) (*KubernetesExecutor, error) {
	nodeName := strings.TrimSpace(os.Getenv("JUDGE_KUBERNETES_NODE_NAME"))
	cgroupRoot := strings.TrimSpace(os.Getenv("JUDGE_KUBERNETES_CGROUP_ROOT"))
	if (nodeName == "") != (cgroupRoot == "") {
		return nil, fmt.Errorf("configure both JUDGE_KUBERNETES_NODE_NAME and JUDGE_KUBERNETES_CGROUP_ROOT for sandbox OOM detection")
	}
	if cgroupRoot != "" {
		if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers")); err != nil {
			return nil, fmt.Errorf("read the node's cgroup v2 mount: %w", err)
		}
	}
	namespace, err := os.ReadFile(filepath.Join(credentialsPath, "namespace"))
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes namespace: %w", err)
	}
	ca, err := os.ReadFile(filepath.Join(credentialsPath, "ca.crt"))
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("invalid Kubernetes CA")
	}
	return &KubernetesExecutor{
		client: &http.Client{
			Timeout:       5 * time.Second,
			Transport:     &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		namespace: strings.TrimSpace(string(namespace)), workspace: workspace, pvc: pvc,
		nodeName: nodeName, cgroupRoot: cgroupRoot,
	}, nil
}

func (e *KubernetesExecutor) CheckReady(ctx context.Context) error {
	return e.request(ctx, http.MethodGet, "/apis/node.k8s.io/v1/runtimeclasses/"+kubernetesRuntimeClass, nil, nil)
}

func (e *KubernetesExecutor) request(ctx context.Context, method, path string, payload, result any) error {
	token, err := os.ReadFile(filepath.Join(credentialsPath, "token"))
	if err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://kubernetes.default.svc"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Content-Type", "application/json")
	response, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		var status meta.Status
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&status)
		return fmt.Errorf("Kubernetes %s %s: HTTP %d (%s)", method, path, response.StatusCode, status.Reason)
	}
	if result != nil {
		return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(result)
	}
	_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	return err
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
	jobsPath := "/apis/batch/v1/namespaces/" + e.namespace + "/jobs"
	deletePolicy := meta.DeletePropagationBackground
	// The request can fail after the API server has created the job.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = e.request(cleanup, http.MethodDelete, jobsPath+"/"+name, meta.DeleteOptions{PropagationPolicy: &deletePolicy}, nil)
	}()
	var created batch.Job
	if err := e.request(ctx, http.MethodPost, jobsPath+"?fieldValidation=Strict", job, &created); err != nil {
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
		if err == nil && !needsOOMObservation(report) {
			return report, nil
		}
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		var podList core.PodList
		podsPath := "/api/v1/namespaces/" + e.namespace + "/pods?labelSelector=batch.kubernetes.io%2Fjob-name%3D" + name
		if err := e.request(deadline, http.MethodGet, podsPath, nil, &podList); err != nil {
			return nil, err
		}
		for _, pod := range podList.Items {
			if !ownsSandboxPod(created, pod) {
				continue
			}
			var terminal *core.ContainerStateTerminated
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name == "sandbox" {
					terminal = status.State.Terminated
					break
				}
			}
			if terminal != nil {
				// The trusted report is renamed before the supervisor exits.
				report, err = reportFromFile(filepath.Join(reportDir, supervisor.ResultFile), req.OutputKB*1024)
				if err != nil && !os.IsNotExist(err) {
					return nil, err
				}
			}
			if terminal != nil && terminal.Reason == "OOMKilled" {
				if report == nil {
					return &ExecuteResult{ExitCode: int(terminal.ExitCode), OOMKilled: true, TimeUnavailable: true}, nil
				}
				report.OOMKilled = true
				return report, nil
			}
			if report != nil {
				if needsOOMObservation(report) && e.cgroupRoot != "" {
					report.OOMKilled, err = e.sandboxOOM(pod)
					if err != nil {
						return nil, err
					}
				}
				return report, nil
			}
			if terminal != nil {
				if terminal.ExitCode == 137 && e.cgroupRoot != "" {
					killed, err := e.sandboxOOM(pod)
					if err != nil {
						return nil, err
					}
					if killed {
						return &ExecuteResult{ExitCode: 137, OOMKilled: true, TimeUnavailable: true}, nil
					}
				}
				return nil, fmt.Errorf("Kubernetes sandbox exited without a report (%s, exit %d)", terminal.Reason, terminal.ExitCode)
			}
		}
		select {
		case <-deadline.Done():
			return nil, fmt.Errorf("Kubernetes sandbox exceeded infrastructure deadline: %w", deadline.Err())
		case <-ticker.C:
		}
	}
}

func needsOOMObservation(report *ExecuteResult) bool {
	return report.ExitCode == 137 && !report.TimedOut && !report.OutputExceeded
}

func ownsSandboxPod(job batch.Job, pod core.Pod) bool {
	owner := meta.GetControllerOf(&pod)
	return owner != nil && owner.Kind == "Job" && owner.UID == job.UID
}

func (e *KubernetesExecutor) sandboxOOM(pod core.Pod) (bool, error) {
	uid := string(pod.UID)
	if pod.Spec.NodeName != e.nodeName || uid == "" || strings.Trim(uid, "0123456789abcdef-") != "" {
		return false, fmt.Errorf("cannot identify the sandbox cgroup for pod %s", pod.Name)
	}
	systemdUID := strings.ReplaceAll(uid, "-", "_")
	for _, qos := range []string{"", "burstable", "besteffort"} {
		sliceParent := "kubepods.slice"
		sliceName := "kubepods"
		if qos != "" {
			sliceName += "-" + qos
			sliceParent = filepath.Join(sliceParent, sliceName+".slice")
		}
		dirs := []string{
			filepath.Join(e.cgroupRoot, "kubepods", qos, "pod"+uid),
			filepath.Join(e.cgroupRoot, sliceParent, sliceName+"-pod"+systemdUID+".slice"),
		}
		for _, dir := range dirs {
			data, err := os.ReadFile(filepath.Join(dir, "memory.events"))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return false, fmt.Errorf("read sandbox memory events: %w", err)
			}
			return memoryLimitKilled(data)
		}
	}
	return false, fmt.Errorf("memory events unavailable for sandbox pod %s", pod.Name)
}

func memoryLimitKilled(data []byte) (bool, error) {
	counts := map[string]uint64{}
	for _, row := range strings.Split(string(data), "\n") {
		fields := strings.Fields(row)
		if len(fields) != 2 || (fields[0] != "oom" && fields[0] != "oom_kill") {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return false, fmt.Errorf("invalid sandbox memory event counter: %w", err)
		}
		counts[fields[0]] = value
	}
	if len(counts) != 2 {
		return false, fmt.Errorf("sandbox memory events lack OOM counters")
	}
	return counts["oom"] > 0 && counts["oom_kill"] > 0, nil
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
		"-pids", strconv.Itoa(req.PidsLimit)}
	var affinity *core.Affinity
	if e.nodeName != "" {
		// Keep the dedicated cgroup alive until the executor reads its final events.
		affinity = &core.Affinity{NodeAffinity: &core.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &core.NodeSelector{NodeSelectorTerms: []core.NodeSelectorTerm{{
				MatchFields: []core.NodeSelectorRequirement{{Key: "metadata.name", Operator: core.NodeSelectorOpIn, Values: []string{e.nodeName}}},
			}}},
		}}
	} else {
		args = append(args, "-exit-after-report")
	}
	args = append(args, "--")
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
					Affinity:         affinity,
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
