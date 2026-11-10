package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/controller"
	"github.com/wsq2333/curvine-operator/internal/curvine/v054"
	"github.com/wsq2333/curvine-operator/internal/observe"
	"github.com/wsq2333/curvine-operator/internal/telemetry"
	"github.com/wsq2333/curvine-operator/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	runtimemetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	namespace := flag.String("watch-namespace", os.Getenv("POD_NAMESPACE"), "required namespace to observe and hold the leader lease")
	metricsAddress := flag.String("metrics-bind-address", "127.0.0.1:8080", "metrics address; 0 disables metrics")
	healthAddress := flag.String("health-probe-bind-address", ":8081", "manager health probe address")
	leaderElection := flag.Bool("leader-elect", true, "enable leader election")
	poll := flag.Duration("poll-interval", 30*time.Second, "application observation interval")
	timeout := flag.Duration("observation-timeout", 5*time.Second, "deadline for each observer call")
	contract := flag.String("workload-contract", "", "opt in to workload management using "+workload.Contract+" or "+v054.Contract+"; empty observes only")
	observerContract := flag.String("observer-contract", "", "application observer contract; the v0.5.4-alpha workload contract selects its observer automatically")
	maxAge := flag.Duration("max-observation-age", 90*time.Second, "maximum accepted sample age")
	existingExpansion := flag.Bool("enable-existing-expansion", false, "opt in to retained worker-only expansion of pinned existing read-cache deployments")
	opts := zap.Options{Development: false}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	if *namespace == "" {
		return fmt.Errorf("--watch-namespace or POD_NAMESPACE is required; cluster-wide observation is not enabled")
	}
	if flag.NArg() != 0 || *poll <= 0 || *timeout <= 0 || *maxAge <= 0 || *timeout > *poll {
		return fmt.Errorf("invalid arguments: durations must be positive and observation-timeout must not exceed poll-interval")
	}
	observer, err := selectObserver(*contract, *observerContract)
	if err != nil {
		return err
	}
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		return err
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		return err
	}
	if err := policyv1.AddToScheme(scheme); err != nil {
		return err
	}
	config, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(config, ctrl.Options{
		Scheme: scheme, Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{*namespace: {}}},
		Metrics: metricsserver.Options{BindAddress: *metricsAddress}, HealthProbeBindAddress: *healthAddress,
		LeaderElection: *leaderElection, LeaderElectionNamespace: *namespace,
		LeaderElectionID: "curvine-operator.storage.curvine.io",
	})
	if err != nil {
		return err
	}
	runtimemetrics.Registry.MustRegister(&telemetry.Collector{Reader: mgr.GetClient(), MaxAge: *maxAge})
	reconciler := &controller.ClusterReconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Observer: observer,
		WorkloadContract: *contract,
		PollInterval:     *poll, ObservationTimeout: *timeout, MaxObservationAge: *maxAge,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return err
	}
	if *existingExpansion {
		expansion := &controller.WorkerExpansionReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Observer: v054.New(nil), PollInterval: *poll}
		if err := expansion.SetupWithManager(mgr); err != nil {
			return err
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	readiness := &cacheReadiness{cache: mgr.GetCache()}
	if err := mgr.Add(readiness); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("cache", readiness.Check); err != nil {
		return err
	}
	return mgr.Start(ctrl.SetupSignalHandler())
}

// Standby replicas may be ready once their cache is synced; this endpoint does
// not claim leadership or application/data health.
type cacheReadiness struct {
	cache cache.Cache
	ready atomic.Bool
}

func (r *cacheReadiness) NeedLeaderElection() bool { return false }
func (r *cacheReadiness) Start(ctx context.Context) error {
	// Register the actual CR informer before waiting: an empty cache may report
	// synced before the controller starts its watches (e.g. on a standby).
	if _, err := r.cache.GetInformer(ctx, &api.CurvineCluster{}); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if r.cache.WaitForCacheSync(ctx) {
		r.ready.Store(true)
		<-ctx.Done()
		r.ready.Store(false)
	}
	return nil
}
func (r *cacheReadiness) Check(*http.Request) error {
	if !r.ready.Load() {
		return errors.New("informer cache is not synced")
	}
	return nil
}

// Observer-only mode is useful for preflight against an already running cluster.
func selectObserver(runtimeContract, observerContract string) (observe.Observer, error) {
	if runtimeContract != "" && !workload.SupportedContract(runtimeContract) {
		return nil, fmt.Errorf("unsupported workload contract")
	}
	if observerContract == "" && runtimeContract == v054.Contract {
		observerContract = v054.Contract
	}
	if observerContract == "" {
		return observe.Unsupported{}, nil
	}
	if observerContract != v054.Contract || (runtimeContract != "" && runtimeContract != observerContract) {
		return nil, fmt.Errorf("unsupported or mismatched observer and workload contracts")
	}
	return v054.New(nil), nil
}
