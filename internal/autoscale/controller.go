package autoscale

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"code-fight-judge/internal/kube"
	"code-fight-judge/internal/storage"

	"github.com/jackc/pgx/v5/pgxpool"
	scaleapi "k8s.io/api/autoscaling/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const calibrationInterval = 30 * time.Second
const calibrationWindow = 30 * time.Minute

func Run(ctx context.Context, args []string) error {
	var p policy
	var deployment string
	var interval, cooldown time.Duration
	flags := flag.NewFlagSet("autoscale", flag.ContinueOnError)
	flags.StringVar(&deployment, "deployment", "", "Judge Deployment name")
	flags.IntVar(&p.workersPerPod, "workers-per-pod", 1, "Execution workers in each Judge Pod")
	flags.IntVar(&p.minReplicas, "min-replicas", 1, "Minimum Judge replicas")
	flags.IntVar(&p.maxReplicas, "max-replicas", 4, "Maximum Judge replicas")
	flags.Float64Var(&p.utilization, "utilization", 0.8, "Plan for this fraction of worker capacity")
	flags.DurationVar(&p.arrivalWindow, "arrival-window", time.Minute, "Window for estimating arrival rate")
	flags.DurationVar(&p.drainTarget, "drain-target", 30*time.Second, "Target time to clear the backlog at requested capacity")
	flags.DurationVar(&interval, "interval", 5*time.Second, "Interval between scaling decisions")
	flags.DurationVar(&cooldown, "idle-cooldown", time.Minute, "Minimum continuous idle time before reducing replicas")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || len(validation.IsDNS1123Subdomain(deployment)) != 0 || p.workersPerPod < 1 ||
		p.minReplicas < 1 || p.maxReplicas < p.minReplicas || p.maxReplicas > math.MaxInt32 ||
		math.IsNaN(p.utilization) || p.utilization <= 0 || p.utilization > 1 ||
		interval < time.Second || p.arrivalWindow < interval || p.drainTarget <= 0 || cooldown < interval {
		return fmt.Errorf("invalid autoscaler deployment or policy settings")
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	dbConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return fmt.Errorf("invalid autoscaler database configuration")
	}
	dbConfig.MaxConns = 2
	dbConfig.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	db, err := pgxpool.NewWithConfig(ctx, dbConfig)
	if err != nil {
		return fmt.Errorf("init autoscaler database: %w", err)
	}
	defer db.Close()
	client, err := kube.NewInClusterClient()
	if err != nil {
		return err
	}
	controller := controller{
		policy: p, cooldown: cooldown, repo: storage.NewJobsRepo(db), client: client,
		scalePath: "/apis/apps/v1/namespaces/" + client.Namespace + "/deployments/" + deployment + "/scale",
		log:       slog.New(slog.NewJSONHandler(os.Stdout, nil)),
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		check, cancel := context.WithTimeout(ctx, min(interval, 5*time.Second))
		err := controller.reconcile(check, time.Now())
		cancel()
		if err != nil && ctx.Err() == nil {
			controller.idleSince = time.Time{}
			controller.log.Warn("autoscaler sample failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

type controller struct {
	policy       policy
	cooldown     time.Duration
	repo         *storage.JobsRepo
	client       *kube.Client
	scalePath    string
	log          *slog.Logger
	serviceTimes map[string]serviceTime
	calibratedAt time.Time
	idleSince    time.Time
}

func (c *controller) reconcile(ctx context.Context, now time.Time) error {
	if now.Sub(c.calibratedAt) >= calibrationInterval {
		samples, err := c.repo.RecentExecutionSamples(ctx, calibrationWindow)
		if err != nil {
			return err
		}
		c.serviceTimes = estimateServiceTimes(samples)
		c.calibratedAt = now
	}
	snapshot, err := c.repo.WorkloadSnapshot(ctx, c.policy.arrivalWindow)
	if err != nil {
		return err
	}
	var scale scaleapi.Scale
	if err := c.client.Request(ctx, http.MethodGet, c.scalePath, nil, &scale); err != nil {
		return err
	}
	estimate := c.policy.estimate(snapshot, c.serviceTimes)
	current := scale.Spec.Replicas
	target := c.target(now, current, estimate, snapshot.Running)
	c.log.Info("Judge scale sample", "queued_jobs", estimate.queuedJobs, "running_jobs", snapshot.Running,
		"queued_worker_seconds", estimate.queuedWorkSeconds, "arrival_jobs_per_second", estimate.arrivalJobsPerSec,
		"arrival_worker_seconds_per_second", estimate.arrivalWorkPerSec, "jobs_per_worker_second", estimate.jobsPerWorkerSec,
		"observed_replicas", scale.Status.Replicas, "current_replicas", current,
		"model_replicas", estimate.replicas, "target_replicas", target)
	if target == current {
		return nil
	}
	scale.Spec.Replicas = target
	return c.client.Request(ctx, http.MethodPut, c.scalePath, scale, nil)
}

func (c *controller) target(now time.Time, current int32, estimate estimate, running int64) int32 {
	if estimate.replicas >= current || estimate.queuedJobs > 0 || running > 0 {
		c.idleSince = time.Time{}
		return max(current, estimate.replicas)
	}
	if c.idleSince.IsZero() {
		c.idleSince = now
	}
	if now.Sub(c.idleSince) < c.cooldown {
		return current
	}
	return estimate.replicas
}
