package autoscale

import (
	"math"
	"time"

	"code-fight-judge/internal/languages"
	"code-fight-judge/internal/storage"
)

type serviceTime struct {
	fixedSeconds   float64
	perTestSeconds float64
}

func initialServiceTime(language string) serviceTime {
	fixed := 0.5
	switch language {
	case "go":
		fixed = 20
	case "c", "cpp":
		fixed = 4
	case "typescript":
		fixed = 6
	}
	return serviceTime{fixedSeconds: fixed, perTestSeconds: 2}
}

func estimateServiceTimes(samples []storage.ExecutionSample) map[string]serviceTime {
	type totals struct {
		compileSeconds, testSeconds float64
		jobs, tests                 int
	}
	byLanguage := make(map[string]totals)
	for _, sample := range samples {
		language := languages.NormalizeLanguage(sample.Language)
		sum := byLanguage[language]
		compile := min(sample.CompileSeconds, sample.WallSeconds)
		sum.compileSeconds += compile
		sum.testSeconds += max(0.05*float64(sample.Tests), sample.WallSeconds-compile)
		sum.jobs++
		sum.tests += sample.Tests
		byLanguage[language] = sum
	}
	// A small prior prevents one completion from setting the cost of a language.
	const prior = 3
	estimates := make(map[string]serviceTime, len(byLanguage))
	for language, sum := range byLanguage {
		initial := initialServiceTime(language)
		estimates[language] = serviceTime{
			fixedSeconds:   (sum.compileSeconds + prior*initial.fixedSeconds) / float64(sum.jobs+prior),
			perTestSeconds: (sum.testSeconds + prior*initial.perTestSeconds) / float64(sum.tests+prior),
		}
	}
	return estimates
}

type estimate struct {
	queuedJobs        int64
	queuedWorkSeconds float64
	arrivalJobsPerSec float64
	arrivalWorkPerSec float64
	jobsPerWorkerSec  float64
	replicas          int32
}

type policy struct {
	arrivalWindow time.Duration
	drainTarget   time.Duration
	workersPerPod int
	utilization   float64
	minReplicas   int
	maxReplicas   int
}

func (p policy) estimate(snapshot storage.WorkloadSnapshot, serviceTimes map[string]serviceTime) estimate {
	cost := func(buckets []storage.WorkloadBucket) (int64, float64) {
		var jobs int64
		var seconds float64
		for _, bucket := range buckets {
			language := languages.NormalizeLanguage(bucket.Language)
			service, ok := serviceTimes[language]
			if !ok {
				service = initialServiceTime(language)
			}
			jobs += bucket.Count
			seconds += float64(bucket.Count) * (service.fixedSeconds + service.perTestSeconds*float64(max(1, bucket.Tests)))
		}
		return jobs, seconds
	}
	queued, backlogWork := cost(snapshot.Queued)
	arrivals, arrivalWork := cost(snapshot.Arrivals)
	arrivalRate := float64(arrivals) / p.arrivalWindow.Seconds()
	arrivalWorkRate := arrivalWork / p.arrivalWindow.Seconds()
	slots := (arrivalWorkRate + backlogWork/p.drainTarget.Seconds()) / p.utilization
	replicas := min(float64(p.maxReplicas), max(float64(p.minReplicas), math.Ceil(slots/float64(p.workersPerPod))))
	var throughput float64
	if arrivalWork > 0 {
		throughput = p.utilization * float64(arrivals) / arrivalWork
	} else if backlogWork > 0 {
		throughput = p.utilization * float64(queued) / backlogWork
	}
	return estimate{
		queuedJobs:        queued,
		queuedWorkSeconds: backlogWork,
		arrivalJobsPerSec: arrivalRate,
		arrivalWorkPerSec: arrivalWorkRate,
		jobsPerWorkerSec:  throughput,
		replicas:          int32(replicas),
	}
}
