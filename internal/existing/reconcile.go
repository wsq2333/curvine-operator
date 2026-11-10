package existing

import (
	"context"
	"fmt"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/curvine/v054"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"net"
	"reflect"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

type Observer interface {
	ObserveImported(context.Context, string, string, string, string, string, time.Time) (v054.ImportedReport, error)
}
type Engine struct {
	Client   client.Client
	Reader   client.Reader
	Observer Observer
	Now      func() time.Time
}
type Result struct {
	Phase, Reason, WorkerName string
	Registered                int32
}

func result(phase, reason string) Result { return Result{Phase: phase, Reason: reason} }
func (e *Engine) load(ctx context.Context, c *api.CurvineWorkerExpansion) (Inputs, error) {
	b := c.Spec.Binding
	in := Inputs{Master: &appsv1.StatefulSet{}, Worker: &appsv1.StatefulSet{}, MasterPod: &corev1.Pod{}, WorkerPod: &corev1.Pod{}, Node: &corev1.Node{}, ConfigMaps: map[string]*corev1.ConfigMap{}}
	objects := []struct {
		name   string
		object client.Object
	}{{b.Master.Name, in.Master}, {b.Worker.Name, in.Worker}, {b.Master.Name + "-0", in.MasterPod}, {b.Worker.Name + "-0", in.WorkerPod}}
	for _, v := range objects {
		if err := e.Reader.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: v.name}, v.object); err != nil {
			return in, err
		}
	}
	if err := e.Reader.Get(ctx, client.ObjectKey{Name: b.TargetNode}, in.Node); err != nil {
		return in, err
	}
	for _, p := range b.ConfigMaps {
		cm := &corev1.ConfigMap{}
		if err := e.Reader.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: p.Name}, cm); err != nil {
			return in, err
		}
		in.ConfigMaps[p.Name] = cm
	}
	return in, nil
}
func (e *Engine) current(ctx context.Context, c *api.CurvineWorkerExpansion) bool {
	var latest api.CurvineWorkerExpansion
	return e.Reader.Get(ctx, client.ObjectKeyFromObject(c), &latest) == nil && latest.UID == c.UID && latest.Generation == c.Generation && latest.DeletionTimestamp == nil && reflect.DeepEqual(latest.Spec, c.Spec)
}
func targetIP(n *corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP && net.ParseIP(a.Address) != nil {
			return a.Address
		}
	}
	return ""
}

func (e *Engine) Reconcile(ctx context.Context, c *api.CurvineWorkerExpansion) Result {
	if c.DeletionTimestamp != nil {
		return result("Retained", "DeletionRequested")
	}
	if c.Spec.Validate() != nil {
		return result("Blocked", "SpecInvalid")
	}
	in, err := e.load(ctx, c)
	if err != nil {
		return result("Unknown", "InventoryUnavailable")
	}
	if reason := Validate(c, in); reason != "" {
		return result("Blocked", reason)
	}
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	if e.Observer == nil {
		return result("Unknown", "ObserverUnavailable")
	}
	// A complete source health check is required before creating any Worker.
	addr := fmt.Sprintf("%s-0.%s.%s.svc.cluster.local:8995", in.Master.Name, in.Master.Spec.ServiceName, c.Namespace)
	observedAt := now()
	sample, err := e.Observer.ObserveImported(ctx, in.MasterPod.Status.PodIP, addr, c.Spec.Binding.ClusterID, in.WorkerPod.Status.HostIP, targetIP(in.Node), now())
	if err != nil {
		return result("Unknown", "ApplicationEvidenceUnavailable")
	}
	if now().Sub(observedAt) > 30*time.Second {
		return result("Unknown", "ObservationExpired")
	}
	if !sample.ExistingHealthy {
		return result("Blocked", "ExistingWorkerUnhealthy")
	}
	if !e.current(ctx, c) {
		return result("Blocked", "IntentChanged")
	}
	// Fence mutable external inputs again after HTTP observation.
	fresh, err := e.load(ctx, c)
	if err != nil {
		return result("Unknown", "InventoryUnavailable")
	}
	if reason := Validate(c, fresh); reason != "" {
		return result("Blocked", reason)
	}
	if fresh.MasterPod.UID != in.MasterPod.UID || fresh.WorkerPod.UID != in.WorkerPod.UID || fresh.MasterPod.Status.PodIP != in.MasterPod.Status.PodIP || fresh.WorkerPod.Status.HostIP != in.WorkerPod.Status.HostIP || targetIP(fresh.Node) != targetIP(in.Node) {
		return result("Blocked", "SourceChanged")
	}
	if now().Sub(observedAt) > 30*time.Second {
		return result("Unknown", "ObservationExpired")
	}
	if c.Spec.Replicas == 1 {
		if sample.AddedHealthy {
			return result("Blocked", "UnexpectedExistingWorker")
		}
		return Result{Phase: "PreflightReady", Reason: "BindingVerified", Registered: sample.Registered}
	}
	// A non-expiring reservation serializes expansion CRs for the same source.
	// Retention deliberately requires explicit retirement after external drain.
	leaseName := "curvine-expand-" + Hash(c.Spec.Binding.Worker.UID)[:24]
	var lease coordinationv1.Lease
	key := client.ObjectKey{Namespace: c.Namespace, Name: leaseName}
	err = e.Reader.Get(ctx, key, &lease)
	if apierrors.IsNotFound(err) {
		if sample.AddedHealthy {
			return result("Blocked", "UnexpectedExistingWorker")
		}
		if !e.current(ctx, c) {
			return result("Blocked", "IntentChanged")
		}
		lease = coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: leaseName, Namespace: c.Namespace}, Spec: coordinationv1.LeaseSpec{HolderIdentity: ptr.To(string(c.UID))}}
		if err = e.Client.Create(ctx, &lease); err != nil {
			return result("Blocked", "ReservationConflict")
		}
		return result("Progressing", "ExpansionReserved")
	}
	if err != nil {
		return result("Unknown", "ReservationUnavailable")
	}
	if lease.DeletionTimestamp != nil || lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != string(c.UID) {
		return result("Blocked", "ReservationConflict")
	}
	canWrite := func() bool {
		var current coordinationv1.Lease
		return e.current(ctx, c) && e.Reader.Get(ctx, key, &current) == nil && current.UID == lease.UID && current.DeletionTimestamp == nil && current.Spec.HolderIdentity != nil && *current.Spec.HolderIdentity == string(c.UID) && now().Sub(observedAt) <= 30*time.Second
	}
	want, service, guard := Build(c, in.Worker)
	// Server defaults are evaluated before comparison, never repaired silently.
	var gotService corev1.Service
	err = e.Reader.Get(ctx, client.ObjectKeyFromObject(service), &gotService)
	if apierrors.IsNotFound(err) {
		if !canWrite() {
			return result("Blocked", "IntentChanged")
		}
		if err = e.Client.Create(ctx, service); err != nil {
			return result("Blocked", "ServiceCreateFailed")
		}
		return result("Progressing", "ServiceCreated")
	}
	if err != nil {
		return result("Unknown", "InventoryUnavailable")
	}
	if gotService.DeletionTimestamp != nil || len(gotService.OwnerReferences) != 0 || gotService.Labels[BindingLabel] != string(c.UID) || gotService.Annotations[FingerprintAnnotation] != Hash(c.Spec.Binding) || gotService.Spec.ClusterIP != corev1.ClusterIPNone || !reflect.DeepEqual(gotService.Spec.Selector, service.Spec.Selector) {
		return result("Blocked", "ServiceConflict")
	}
	configs := configurationSnapshots(c, in)
	configs = append(configs, guard)
	for _, wanted := range configs {
		var got corev1.ConfigMap
		err = e.Reader.Get(ctx, client.ObjectKeyFromObject(wanted), &got)
		if apierrors.IsNotFound(err) {
			if !canWrite() {
				return result("Blocked", "IntentChanged")
			}
			if err = e.Client.Create(ctx, wanted); err != nil {
				return result("Blocked", "ConfigurationCreateFailed")
			}
			return result("Progressing", "ConfigurationSnapshotCreated")
		}
		if err != nil {
			return result("Unknown", "InventoryUnavailable")
		}
		if got.DeletionTimestamp != nil || len(got.OwnerReferences) != 0 || got.Labels[BindingLabel] != string(c.UID) || got.Annotations[FingerprintAnnotation] != Hash(c.Spec.Binding) || got.Immutable == nil || !*got.Immutable || !reflect.DeepEqual(got.Data, wanted.Data) || !reflect.DeepEqual(got.BinaryData, wanted.BinaryData) {
			return result("Blocked", "ConfigurationSnapshotConflict")
		}
	}
	var live appsv1.StatefulSet
	err = e.Reader.Get(ctx, client.ObjectKeyFromObject(want), &live)
	if apierrors.IsNotFound(err) {
		if sample.AddedHealthy {
			return result("Blocked", "UnexpectedExistingWorker")
		}
		check := want.DeepCopy()
		if err = e.Client.Create(ctx, check, client.DryRunAll); err != nil {
			return result("Blocked", "AdmissionRejected")
		}
		if !canWrite() {
			return result("Blocked", "IntentChanged")
		}
		if err = e.Client.Create(ctx, want); err != nil {
			return result("Blocked", "WorkerCreateFailed")
		}
		return Result{Phase: "Progressing", Reason: "WorkerCreated", WorkerName: want.Name + "-0", Registered: sample.Registered}
	}
	if err != nil {
		return result("Unknown", "InventoryUnavailable")
	}
	// Compare source-derived intent against live template using server defaults
	// already present in the pinned source template. Metadata is separately fenced.
	if !matches(c, &live, want) {
		return result("Blocked", "WorkerDrift")
	}
	out := Result{Phase: "Progressing", Reason: "WaitingForWorkerPod", WorkerName: live.Name + "-0", Registered: sample.Registered}
	var pod corev1.Pod
	if err = e.Reader.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: out.WorkerName}, &pod); err != nil {
		return out
	}
	if pod.Spec.NodeName == "" {
		out.Reason = "WaitingForScheduling"
		return out
	}
	if pod.Labels[BindingLabel] != string(c.UID) || pod.Spec.NodeName != c.Spec.Binding.TargetNode || pod.Status.HostIP != targetIP(in.Node) {
		return result("Blocked", "WorkerPlacementMismatch")
	}
	if !readyPod(&pod, &live) {
		out.Reason = "WorkerNotReady"
		for _, s := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
			if s.State.Waiting != nil {
				switch s.State.Waiting.Reason {
				case "ErrImagePull", "ImagePullBackOff":
					out.Reason = "ImagePullFailed"
				case "CrashLoopBackOff":
					out.Reason = "ContainerCrashLoop"
				}
			}
			if s.State.Terminated != nil && s.State.Terminated.ExitCode != 0 {
				out.Reason = "DiskGuardOrContainerFailed"
			}
		}
		return out
	}
	if !imageMatches(&pod, c.Spec.Binding.Image) {
		return result("Blocked", "RuntimeImageUnverified")
	}
	out.Reason = "WaitingForRegistration"
	if sample.AddedHealthy && sample.Registered == 2 {
		out.Phase = "Registered"
		out.Reason = "RuntimeAndRegistrationVerified"
	}
	return out
}
