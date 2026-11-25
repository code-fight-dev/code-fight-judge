package autoscale

import (
	"math"
	"testing"
	"time"

	"code-fight-judge/internal/storage"
)

func TestEstimateUsesLanguageTestCountAndArrivalMix(t *testing.T) {
	p := policy{arrivalWindow: 10 * time.Second, drainTarget: 10 * time.Second,
		workersPerPod: 2, utilization: 0.5, minReplicas: 1, maxReplicas: 8}
	times := map[string]serviceTime{"go": {fixedSeconds: 5, perTestSeconds: 2}, "python": {perTestSeconds: 1}}
	snapshot := storage.WorkloadSnapshot{
		Queued:   []storage.WorkloadBucket{{Language: "go", Tests: 3, Count: 2}, {Language: "python", Tests: 2, Count: 4}},
		Arrivals: []storage.WorkloadBucket{{Language: "python", Tests: 2, Count: 5}},
	}
	got := p.estimate(snapshot, times)
	if got.queuedJobs != 6 || got.queuedWorkSeconds != 30 || got.arrivalJobsPerSec != 0.5 ||
		got.arrivalWorkPerSec != 1 || got.jobsPerWorkerSec != 0.25 || got.replicas != 4 {
		t.Fatalf("unexpected estimate: %+v", got)
	}
	p.maxReplicas = 3
	if got := p.estimate(snapshot, times).replicas; got != 3 {
		t.Fatalf("replica cap = %d, want 3", got)
	}
	if got := p.estimate(storage.WorkloadSnapshot{}, nil).replicas; got != 1 {
		t.Fatalf("idle replicas = %d, want 1", got)
	}
}

func TestEstimatesIncludeSandboxOverhead(t *testing.T) {
	times := estimateServiceTimes([]storage.ExecutionSample{
		{Language: "go", WallSeconds: 15, CompileSeconds: 9, Tests: 3},
		{Language: "go", WallSeconds: 13, CompileSeconds: 3, Tests: 1},
	})
	got := times["go"]
	if math.Abs(got.fixedSeconds-14.4) > 0.00001 || math.Abs(got.perTestSeconds-22.0/7) > 0.00001 {
		t.Fatalf("unexpected service cost: %+v", got)
	}
}

func TestScaleDownWaitsForContinuousIdle(t *testing.T) {
	c := controller{cooldown: 20 * time.Second}
	now := time.Now()
	low := estimate{replicas: 1}
	if got := c.target(now, 1, estimate{replicas: 3, queuedJobs: 10}, 1); got != 3 {
		t.Fatalf("scale-up target = %d, want 3", got)
	}
	if got := c.target(now, 3, low, 0); got != 3 {
		t.Fatalf("scaled down before the cooldown: %d", got)
	}
	if got := c.target(now.Add(21*time.Second), 3, low, 1); got != 3 {
		t.Fatalf("scaled down while busy: %d", got)
	}
	if got := c.target(now.Add(22*time.Second), 3, low, 0); got != 3 {
		t.Fatalf("busy sample did not reset idle time: %d", got)
	}
	if got := c.target(now.Add(42*time.Second), 3, low, 0); got != 1 {
		t.Fatalf("did not scale down after continuous idle: %d", got)
	}
}
