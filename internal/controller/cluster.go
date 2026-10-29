package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/observe"
	"github.com/wsq2333/curvine-operator/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	reconcilepkg "sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	ResourcesReady   = "ResourcesReady"
	Progressing      = "Progressing"
	SpecValid        = "SpecValid"
	ObservationReady = "ObservationReady"
	QuorumHealthy    = "QuorumHealthy"
	WorkersHealthy   = "WorkersHealthy"
	DataAvailable    = "DataAvailable"
	Ready            = "Ready"
)

type ClusterReconciler struct {
	client.Client
	// APIReader bypasses the cache before writing status to reject stale work.
	APIReader          client.Reader
	Observer           observe.Observer
	PollInterval       time.Duration
	ObservationTimeout time.Duration
	MaxObservationAge  time.Duration
	Now                func() time.Time
	WorkloadContract   string
}

func (r *ClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	b := ctrl.NewControllerManagedBy(mgr).
		For(&api.CurvineCluster{}, builder.WithPredicates(predicate.GenerationChangedPredicate{}))
	if r.WorkloadContract != "" {
		b = b.Owns(&appsv1.StatefulSet{}).Owns(&corev1.Service{}).
			Owns(&corev1.ConfigMap{}).Owns(&policyv1.PodDisruptionBudget{}).
			Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(clusterForChild)).
			Watches(&corev1.PersistentVolumeClaim{}, handler.EnqueueRequestsFromMapFunc(clusterForChild))
	}
	return b.Complete(r)
}

// Pods are owned by StatefulSets and retained PVCs deliberately have no owner.
// Labels route events only; the uncached ownership checks authorize all writes.
func clusterForChild(_ context.Context, obj client.Object) []reconcilepkg.Request {
	name := obj.GetLabels()[workload.ClusterLabel]
	if name == "" || obj.GetLabels()[workload.UIDLabel] == "" {
		return nil
	}
	return []reconcilepkg.Request{{NamespacedName: client.ObjectKey{Namespace: obj.GetNamespace(), Name: name}}}
}

func positive(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func (r *ClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var cluster api.CurvineCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !cluster.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	interval := positive(r.PollInterval, 30*time.Second)
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	target := observe.TargetFor(&cluster)
	valid := cluster.Intent().Validate() == nil
	reason, message := "SpecInvalid", "The specification does not satisfy the supported intent contract"
	var snapshot *observe.Snapshot
	if valid {
		observer := r.Observer
		if observer == nil {
			observer = observe.Unsupported{}
		}
		observationCtx, cancel := context.WithTimeout(ctx, positive(r.ObservationTimeout, 5*time.Second))
		sample, err := observer.Observe(observationCtx, target)
		if observationCtx.Err() != nil {
			err = observationCtx.Err()
		}
		cancel()
		if ctx.Err() != nil {
			return ctrl.Result{}, ctx.Err()
		}
		switch {
		case errors.Is(err, observe.ErrUnsupported):
			reason, message = "UnsupportedCapability", "No compatible Curvine observer is configured"
		case errors.Is(err, context.DeadlineExceeded):
			reason, message = "ObservationTimeout", "The application observation deadline was exceeded"
		case err != nil:
			// Adapter errors may include URLs or credentials; never publish raw errors.
			reason, message = "ObservationFailed", "Application state could not be observed; health is unknown"
		case sample.Validate(target, now(), positive(r.MaxObservationAge, 90*time.Second)) != nil:
			reason, message = "InvalidObservation", "The sample is stale, incomplete or does not match this resource generation"
		default:
			snapshot = &sample
			reason, message = "Observed", "A current application sample is available"
		}
	}
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	var latest api.CurvineCluster
	if err := reader.Get(ctx, req.NamespacedName, &latest); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !latest.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if latest.UID != cluster.UID || latest.Generation != cluster.Generation {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	resources := workload.Result{Status: metav1.ConditionUnknown, Reason: "WorkloadManagementDisabled", Message: "No workload runtime contract is selected"}
	var resourceErr error
	if r.WorkloadContract != "" {
		if valid {
			engine := workload.Engine{Client: r.Client, Reader: reader, Contract: r.WorkloadContract, Now: now, MaxObservationAge: r.MaxObservationAge}
			resources, resourceErr = engine.Reconcile(ctx, &latest, snapshot)
		} else {
			resources = workload.Result{Status: metav1.ConditionFalse, Reason: "SpecInvalid", Message: "Invalid specifications cannot be applied"}
		}
	}
	// Resource preflight may take time; do not publish a sample that expired
	// while validating/applying Kubernetes resources.
	if snapshot != nil && snapshot.Validate(target, now(), positive(r.MaxObservationAge, 90*time.Second)) != nil {
		snapshot = nil
		reason, message = "InvalidObservation", "The sample expired while reconciling Kubernetes resources"
	}
	base := latest.DeepCopy()
	latest.Status.ObservedGeneration = latest.Generation
	set := func(typ string, status metav1.ConditionStatus, why, detail string) {
		meta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: typ, Status: status, Reason: why, Message: detail,
			ObservedGeneration: latest.Generation, LastTransitionTime: metav1.NewTime(now()),
		})
	}
	if valid {
		set(SpecValid, metav1.ConditionTrue, "Validated", "The intent satisfies the supported specification contract")
	} else {
		set(SpecValid, metav1.ConditionFalse, reason, message)
	}
	if snapshot == nil {
		latest.Status.Observation = nil
		set(ObservationReady, metav1.ConditionUnknown, reason, message)
		for _, typ := range []string{QuorumHealthy, WorkersHealthy, DataAvailable} {
			set(typ, metav1.ConditionUnknown, reason, message)
		}
		status := metav1.ConditionUnknown
		if !valid {
			status = metav1.ConditionFalse
		}
		set(Ready, status, reason, message)
	} else {
		timestamp := metav1.NewTime(snapshot.ObservedAt)
		latest.Status.Observation = &api.ObservationStatus{SampleTime: timestamp, Version: snapshot.Version, RegisteredWorkers: snapshot.RegisteredWorkers}
		latest.Status.LastSuccessfulObservationTime = &timestamp
		set(ObservationReady, metav1.ConditionTrue, reason, message)
		for _, check := range []struct {
			typ    string
			health observe.Health
		}{
			{QuorumHealthy, snapshot.Quorum}, {WorkersHealthy, snapshot.Workers}, {DataAvailable, snapshot.DataAvailability},
		} {
			status, why, detail := healthCondition(check.health)
			set(check.typ, status, why, detail)
		}
		state := observe.Healthy
		for _, h := range []observe.Health{snapshot.Quorum, snapshot.Workers, snapshot.DataAvailability} {
			if h == observe.Unhealthy {
				state = observe.Unhealthy
				break
			}
			if h == observe.Unknown {
				state = observe.Unknown
			}
		}
		status, why, detail := healthCondition(state)
		set(Ready, status, why, detail)
	}
	set(ResourcesReady, resources.Status, resources.Reason, resources.Message)
	progressing := metav1.ConditionFalse
	if resources.Progressing {
		progressing = metav1.ConditionTrue
	}
	set(Progressing, progressing, resources.Reason, resources.Message)
	if r.WorkloadContract != "" && resources.Status != metav1.ConditionTrue {
		set(Ready, resources.Status, resources.Reason, resources.Message)
	}
	if resources.Reason == "ReconcilingResources" {
		interval = time.Second
	}
	// Keep raw API errors out of logs/status; retries still use queue backoff.
	if resourceErr != nil {
		resourceErr = fmt.Errorf("workload reconciliation: %s", resources.Reason)
	}
	if equality.Semantic.DeepEqual(base.Status, latest.Status) {
		return ctrl.Result{RequeueAfter: interval}, resourceErr
	}
	err := r.Status().Patch(ctx, &latest, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	if apierrors.IsConflict(err) {
		// Re-observe rather than retrying an old application sample over new intent.
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	if err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return ctrl.Result{RequeueAfter: interval}, resourceErr
}

func healthCondition(h observe.Health) (metav1.ConditionStatus, string, string) {
	switch h {
	case observe.Healthy:
		return metav1.ConditionTrue, "Healthy", "The application observer reports this health requirement is satisfied"
	case observe.Unhealthy:
		return metav1.ConditionFalse, "Unhealthy", "The application observer reports this health requirement is not satisfied"
	default:
		return metav1.ConditionUnknown, "HealthUnknown", "The application observer cannot establish this health requirement"
	}
}
