package controller

import (
	"context"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/existing"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	reconcilepkg "sigs.k8s.io/controller-runtime/pkg/reconcile"
	"time"
)

type WorkerExpansionReconciler struct {
	client.Client
	APIReader    client.Reader
	Observer     existing.Observer
	PollInterval time.Duration
}

func (r *WorkerExpansionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	route := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcilepkg.Request {
		if o.GetLabels()[existing.BindingLabel] == "" || o.GetLabels()[existing.NameLabel] == "" {
			return nil
		}
		return []reconcilepkg.Request{{NamespacedName: client.ObjectKey{Namespace: o.GetNamespace(), Name: o.GetLabels()[existing.NameLabel]}}}
	})
	return ctrl.NewControllerManagedBy(mgr).For(&api.CurvineWorkerExpansion{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).Watches(&appsv1.StatefulSet{}, route).Watches(&corev1.Pod{}, route).Complete(r)
}
func (r *WorkerExpansionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	var c api.CurvineWorkerExpansion
	if err := reader.Get(ctx, req.NamespacedName, &c); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if c.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}
	engine := existing.Engine{Client: r.Client, Reader: reader, Observer: r.Observer}
	deadline, cancel := context.WithTimeout(ctx, 20*time.Second)
	out := engine.Reconcile(deadline, &c)
	cancel()
	var current api.CurvineWorkerExpansion
	if err := reader.Get(ctx, req.NamespacedName, &current); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if current.UID != c.UID || current.Generation != c.Generation || current.DeletionTimestamp != nil {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	base := current.DeepCopy()
	now := metav1.Now()
	progress := current.Status.LastProgressTime
	if progress == nil || current.Status.ObservedGeneration != current.Generation || current.Status.Registered != out.Registered || current.Status.Phase != out.Phase {
		progress = &now
	}
	current.Status = api.WorkerExpansionStatus{ObservedGeneration: current.Generation, Phase: out.Phase, Reason: out.Reason, WorkerName: out.WorkerName, Registered: out.Registered, LastProgressTime: progress, SampleTime: &now, Conditions: current.Status.Conditions}
	set := func(name string, status metav1.ConditionStatus, reason, message string) {
		meta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{Type: name, Status: status, Reason: reason, Message: message, ObservedGeneration: current.Generation, LastTransitionTime: now})
	}
	status := metav1.ConditionFalse
	if out.Phase == "Registered" || out.Phase == "PreflightReady" {
		status = metav1.ConditionTrue
	} else if out.Phase == "Unknown" {
		status = metav1.ConditionUnknown
	}
	set("AllocationReady", status, out.Reason, "Allocation and registration only; data-path acceptance is separate")
	set("DataPathVerified", metav1.ConditionUnknown, "AcceptanceRequired", "Run the explicit read-only consumer acceptance tool; no automatic workload probes are executed")
	set("ReleaseProvenanceVerified", metav1.ConditionUnknown, "ImportedRuntime", "The imported runtime uses approved image digest and protocol evidence, not upstream release proof")
	stalled := metav1.ConditionFalse
	if out.Phase != "Registered" && out.Phase != "PreflightReady" && time.Since(progress.Time) >= 10*time.Minute {
		stalled = metav1.ConditionTrue
	}
	set("ExpansionStalled", stalled, "ProgressDeadline", "Ten-minute diagnostic deadline; no rollback or deletion")
	err := r.Status().Patch(ctx, &current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{}))
	return ctrl.Result{RequeueAfter: positive(r.PollInterval, 30*time.Second)}, err
}
