package metrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type queueCollector struct {
	snapshot  func(context.Context) (int64, float64, error)
	depth     *prometheus.Desc
	oldest    *prometheus.Desc
	available *prometheus.Desc
}

func (m *JudgeMetrics) RegisterQueue(snapshot func(context.Context) (int64, float64, error)) {
	m.registry.MustRegister(queueCollector{
		snapshot: snapshot,
		depth: prometheus.NewDesc("codefight_judge_queue_depth",
			"Number of Judge jobs eligible for execution in the shared queue.", nil, nil),
		oldest: prometheus.NewDesc("codefight_judge_queue_oldest_job_age_seconds",
			"Seconds since the oldest eligible Judge job was enqueued.", nil, nil),
		available: prometheus.NewDesc("codefight_judge_queue_snapshot_available",
			"Whether the shared Judge queue was read successfully.", nil, nil),
	})
}

func (c queueCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.depth
	ch <- c.oldest
	ch <- c.available
}

func (c queueCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	depth, oldest, err := c.snapshot(ctx)
	if err != nil {
		ch <- prometheus.MustNewConstMetric(c.available, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.available, prometheus.GaugeValue, 1)
	ch <- prometheus.MustNewConstMetric(c.depth, prometheus.GaugeValue, float64(depth))
	ch <- prometheus.MustNewConstMetric(c.oldest, prometheus.GaugeValue, oldest)
}
