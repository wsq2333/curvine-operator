package workload

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/observe"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Result struct {
	Status          metav1.ConditionStatus
	Reason, Message string
	Progressing     bool
}

func pending(reason, message string) Result {
	return Result{Status: metav1.ConditionFalse, Reason: reason, Message: message, Progressing: true}
}
func blocked(reason, message string) Result {
	return Result{Status: metav1.ConditionFalse, Reason: reason, Message: message}
}

type Engine struct {
	Client client.Client
	// Reader must be the uncached APIReader in production. All ownership and
	// incarnation checks use it, including checks immediately before a write.
	Reader            client.Reader
	Contract          string
	Now               func() time.Time
	MaxObservationAge time.Duration
}
type change struct{ desired, current client.Object }

// Reconcile validates the entire resource set before any write, then performs
// at most one mutation. Kubernetes cannot atomically transact a CR and all its
// children: each write is fenced by a fresh parent read and child resourceVersion.
func (e *Engine) Reconcile(ctx context.Context, c *api.CurvineCluster, sample *observe.Snapshot) (Result, error) {
	b, err := Build(c, e.Contract)
	if err != nil {
		return blocked("UnsupportedRuntimeContract", "The specification or selected runtime contract cannot generate workloads"), nil
	}
	if err = e.current(ctx, c); err != nil {
		return pending("IntentChanged", "The resource changed; reconciliation will restart"), err
	}
	existing := make(map[string]client.Object)
	var changes []change
	for _, desired := range b.Objects {
		live := desired.DeepCopyObject().(client.Object)
		err := e.Reader.Get(ctx, client.ObjectKeyFromObject(desired), live)
		if apierrors.IsNotFound(err) {
			changes = append(changes, change{desired: desired})
			continue
		}
		if err != nil {
			return apiFailure(err)
		}
		if issue := checkExisting(c, desired, live); issue != nil {
			return *issue, nil
		}
		existing[key(desired)] = live
	}
	// Validate every retained claim, even when it lies outside the new replica
	// count. Never adopt a claim from a previous CR incarnation or another owner.
	if result, err := e.checkStorage(ctx, c, b); result != nil || err != nil {
		if err != nil {
			return apiFailure(err)
		}
		return *result, nil
	}
	var sets appsv1.StatefulSetList
	if err := e.Reader.List(ctx, &sets, client.InNamespace(c.Namespace), client.MatchingLabels{UIDLabel: string(c.UID)}); err != nil {
		return apiFailure(err)
	}
	wanted := map[string]bool{b.Master.Name: true}
	for _, w := range b.Workers {
		wanted[w.Name] = true
	}
	for _, s := range sets.Items {
		if !wanted[s.Name] {
			return blocked("PoolRemovalBlocked", "An existing StatefulSet is absent from the desired specification"), nil
		}
	}
	// Dry-run the complete bundle first. This checks admission/immutable fields
	// without half-applying a change that is already known to be invalid.
	for _, desired := range b.Objects {
		live := existing[key(desired)]
		if live == nil {
			if err := e.Client.Create(ctx, desired.DeepCopyObject().(client.Object), client.DryRunAll, client.FieldOwner(FieldManager)); err != nil {
				return apiFailure(err)
			}
			continue
		}
		applied, err := e.apply(ctx, desired, live, true)
		if err != nil {
			return apiFailure(err)
		}
		if !sameContent(applied, live) {
			changes = append(changes, change{desired: desired, current: live})
		}
	}
	// Restore dependency order after collecting create and update differences.
	byKey := map[string]change{}
	for _, ch := range changes {
		byKey[key(ch.desired)] = ch
	}
	for _, desired := range b.Objects {
		ch, changed := byKey[key(desired)]
		if !changed {
			continue
		}
		if sts, ok := desired.(*appsv1.StatefulSet); ok && sts.Labels[RoleLabel] == "worker" {
			if !setReady(existing[key(b.Master)], *b.Master.Spec.Replicas) || !e.fresh(c, sample) || sample.Quorum != observe.Healthy {
				return pending("WaitingForQuorum", "Worker creation or changes require ready masters and an observed healthy quorum"), nil
			}
			if ch.current != nil && *sts.Spec.Replicas > *ch.current.(*appsv1.StatefulSet).Spec.Replicas && (sample.Workers != observe.Healthy || sample.DataAvailability != observe.Healthy) {
				return pending("WaitingForApplicationHealth", "Worker expansion requires observed worker health and data availability"), nil
			}
		}
		if err := e.current(ctx, c); err != nil {
			return pending("IntentChanged", "The resource changed; reconciliation will restart"), err
		}
		// Use Create for absence, never Apply: a competing creator must produce
		// AlreadyExists rather than being silently adopted by server-side apply.
		if ch.current == nil {
			err = e.Client.Create(ctx, ch.desired.DeepCopyObject().(client.Object), client.FieldOwner(FieldManager))
		} else {
			_, err = e.apply(ctx, ch.desired, ch.current, false)
		}
		if err != nil {
			return apiFailure(err)
		}
		return pending("ReconcilingResources", "A managed resource was reconciled; waiting for the next observation"), nil
	}
	for _, s := range append([]*appsv1.StatefulSet{b.Master}, b.Workers...) {
		if !setReady(existing[key(s)], *s.Spec.Replicas) {
			return pending("WaitingForStatefulSets", "StatefulSets have not reached their desired generation, revision and ready replica counts"), nil
		}
	}
	// Bound claims are necessary; inspect one namespace list, not one API
	// request per ordinal. Missing claims cannot be inferred from replicas.
	var claims corev1.PersistentVolumeClaimList
	if err := e.Reader.List(ctx, &claims, client.InNamespace(c.Namespace)); err != nil {
		return apiFailure(err)
	}
	bound := map[string]bool{}
	for _, p := range claims.Items {
		bound[p.Name] = p.Status.Phase == corev1.ClaimBound && p.Labels[UIDLabel] == string(c.UID) && p.DeletionTimestamp.IsZero() && len(p.OwnerReferences) == 0
	}
	for _, s := range append([]*appsv1.StatefulSet{b.Master}, b.Workers...) {
		for i := int32(0); i < *s.Spec.Replicas; i++ {
			if !bound[fmt.Sprintf("data-%s-%d", s.Name, i)] {
				return pending("WaitingForVolumes", "Expected persistent volumes are not all bound"), nil
			}
		}
	}

	return Result{Status: metav1.ConditionTrue, Reason: "ResourcesConverged", Message: "Managed resources, StatefulSet revisions and bound claims match the desired specification"}, nil
}

func (e *Engine) fresh(c *api.CurvineCluster, sample *observe.Snapshot) bool {
	if sample == nil {
		return false
	}
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	age := e.MaxObservationAge
	if age <= 0 {
		age = 90 * time.Second
	}
	return sample.Validate(observe.TargetFor(c), now(), age) == nil
}

func key(o client.Object) string { return fmt.Sprintf("%T/%s", o, o.GetName()) }
func owned(c *api.CurvineCluster, o client.Object) bool {
	owner := metav1.GetControllerOf(o)
	return owner != nil && owner.UID == c.UID && owner.Name == c.Name && owner.Kind == "CurvineCluster" && owner.APIVersion == api.APIVersion
}
func checkExisting(c *api.CurvineCluster, desired, live client.Object) *Result {
	fail := func(reason, message string) *Result { r := blocked(reason, message); return &r }
	if !owned(c, live) {
		return fail("OwnershipConflict", "A same-name resource is not controlled by this CurvineCluster UID")
	}
	if !live.GetDeletionTimestamp().IsZero() {
		r := pending("ResourceTerminating", "A managed resource is terminating")
		return &r
	}
	if live.GetAnnotations()[ContractAnnotation] != Contract {
		return fail("RuntimeContractConflict", "An existing resource uses a different or missing runtime contract")
	}
	switch want := desired.(type) {
	case *corev1.ConfigMap:
		got := live.(*corev1.ConfigMap)
		if !reflect.DeepEqual(want.Data, got.Data) || len(got.BinaryData) > 0 {
			return fail("ConfigurationConflict", "The immutable configuration does not match its content hash")
		}
	case *corev1.Service:
		for k := range live.(*corev1.Service).Spec.Selector {
			if _, ok := want.Spec.Selector[k]; !ok {
				return fail("ServiceLayoutConflict", "An unmanaged service selector could hide the managed endpoints")
			}
		}
		if live.(*corev1.Service).Spec.ClusterIP != corev1.ClusterIPNone {
			return fail("ServiceLayoutConflict", "An existing service does not use the required headless layout")
		}
	case *appsv1.StatefulSet:
		got := live.(*appsv1.StatefulSet)
		if got.Spec.Replicas == nil {
			return fail("InvalidWorkload", "The existing replica count is unknown")
		}
		if *got.Spec.Replicas > *want.Spec.Replicas {
			return fail("ScaleInBlocked", "Reducing an existing replica count requires a dedicated safety workflow")
		}
		if want.Labels[RoleLabel] == "master" && *got.Spec.Replicas != *want.Spec.Replicas {
			return fail("MembershipChangeBlocked", "Changing the existing Master replica count requires a Raft membership workflow")
		}
		if got.Spec.ServiceName != want.Spec.ServiceName || got.Spec.PodManagementPolicy != want.Spec.PodManagementPolicy || !reflect.DeepEqual(got.Spec.Selector, want.Spec.Selector) || !sameClaims(want, got) {
			return fail("StorageLayoutConflict", "The StatefulSet identity, selector or persistent storage layout differs")
		}
		if len(got.Spec.Template.Spec.Containers) == 0 {
			return fail("InvalidWorkload", "The Curvine container is missing")
		}
		found := false
		for _, container := range got.Spec.Template.Spec.Containers {
			if container.Name == "curvine" {
				found = true
				if container.Image != c.Spec.Image {
					return fail("UpgradeRequired", "Changing an existing Curvine image requires an upgrade workflow")
				}
			}
		}
		if !found {
			return fail("InvalidWorkload", "The Curvine container is missing")
		}
	}
	return nil
}
func sameClaims(a, b *appsv1.StatefulSet) bool {
	if len(a.Spec.VolumeClaimTemplates) != 1 || len(b.Spec.VolumeClaimTemplates) != 1 {
		return false
	}
	x, y := a.Spec.VolumeClaimTemplates[0], b.Spec.VolumeClaimTemplates[0]
	xq, yq := x.Spec.Resources.Requests[corev1.ResourceStorage], y.Spec.Resources.Requests[corev1.ResourceStorage]
	return x.Name == y.Name && reflect.DeepEqual(x.Spec.StorageClassName, y.Spec.StorageClassName) && reflect.DeepEqual(x.Spec.AccessModes, y.Spec.AccessModes) && xq.Cmp(yq) == 0 && y.Spec.Selector == nil && y.Spec.DataSource == nil && y.Spec.DataSourceRef == nil && (y.Spec.VolumeMode == nil || *y.Spec.VolumeMode == corev1.PersistentVolumeFilesystem) && y.Labels[UIDLabel] == x.Labels[UIDLabel] && len(y.OwnerReferences) == 0
}
func setReady(o client.Object, replicas int32) bool {
	s, ok := o.(*appsv1.StatefulSet)
	return ok && s.Status.ObservedGeneration >= s.Generation && s.Status.ReadyReplicas == replicas && s.Status.UpdatedReplicas == replicas && s.Status.Replicas == replicas && s.Status.CurrentRevision != "" && s.Status.CurrentRevision == s.Status.UpdateRevision
}
func (e *Engine) checkStorage(ctx context.Context, c *api.CurvineCluster, b *Bundle) (*Result, error) {
	var pvcs corev1.PersistentVolumeClaimList
	if err := e.Reader.List(ctx, &pvcs, client.InNamespace(c.Namespace)); err != nil {
		return nil, err
	}
	sets := append([]*appsv1.StatefulSet{b.Master}, b.Workers...)
	for _, p := range pvcs.Items {
		for _, s := range sets {
			ordinal, match := strings.CutPrefix(p.Name, "data-"+s.Name+"-")
			if !match {
				continue
			}
			if _, err := strconv.ParseUint(ordinal, 10, 32); err != nil {
				continue
			}
			expected := s.Spec.VolumeClaimTemplates[0]
			wantSize, gotSize := expected.Spec.Resources.Requests[corev1.ResourceStorage], p.Spec.Resources.Requests[corev1.ResourceStorage]
			if p.Labels[UIDLabel] != string(c.UID) || p.Labels[ClusterLabel] != c.Name || len(p.OwnerReferences) != 0 || !reflect.DeepEqual(p.Spec.StorageClassName, expected.Spec.StorageClassName) || !reflect.DeepEqual(p.Spec.AccessModes, expected.Spec.AccessModes) || wantSize.Cmp(gotSize) != 0 || p.Spec.Selector != nil || p.Spec.DataSource != nil || p.Spec.DataSourceRef != nil || (p.Spec.VolumeMode != nil && *p.Spec.VolumeMode != corev1.PersistentVolumeFilesystem) {
				r := blocked("RetainedVolumeConflict", "An existing claim has a different identity, owner or storage contract; explicit recovery is required")
				return &r, nil
			}
			if !p.DeletionTimestamp.IsZero() || p.Status.Phase == corev1.ClaimLost {
				r := blocked("VolumeUnavailable", "An existing data claim is terminating or lost")
				return &r, nil
			}
		}
	}
	return nil, nil
}
func (e *Engine) current(ctx context.Context, c *api.CurvineCluster) error {
	var latest api.CurvineCluster
	if err := e.Reader.Get(ctx, client.ObjectKeyFromObject(c), &latest); err != nil {
		return err
	}
	if latest.UID != c.UID || latest.Generation != c.Generation || !latest.DeletionTimestamp.IsZero() {
		return apierrors.NewConflict(api.GroupVersion.WithResource("curvineclusters").GroupResource(), c.Name, fmt.Errorf("intent changed"))
	}
	return nil
}
func (e *Engine) apply(ctx context.Context, desired, live client.Object, dry bool) (client.Object, error) {
	// RV and UID preconditions also reject a replacement that happens after the
	// ownership check. The apply document contains only fields managed by Build.
	obj := desired.DeepCopyObject().(client.Object)
	obj.SetResourceVersion(live.GetResourceVersion())
	obj.SetUID(live.GetUID())
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	opts := []client.PatchOption{client.FieldOwner(FieldManager), client.ForceOwnership}
	if dry {
		opts = append(opts, client.DryRunAll)
	}
	err = e.Client.Patch(ctx, obj, client.RawPatch(types.ApplyPatchType, data), opts...)
	return obj, err
}
func sameContent(a, b client.Object) bool {
	normalize := func(o client.Object) map[string]any {
		data, _ := json.Marshal(o)
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		delete(m, "status")
		delete(m, "apiVersion")
		delete(m, "kind")
		unstructured.RemoveNestedField(m, "metadata", "managedFields")
		unstructured.RemoveNestedField(m, "metadata", "resourceVersion")
		unstructured.RemoveNestedField(m, "metadata", "generation")
		return m
	}
	return reflect.DeepEqual(normalize(a), normalize(b))
}
func apiFailure(err error) (Result, error) {
	switch {
	case apierrors.IsForbidden(err):
		return blocked("PermissionDenied", "The Kubernetes API denied access to a required resource"), err
	case apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err):
		return pending("ResourceConflict", "A concurrent resource change requires a fresh reconciliation"), err
	case apierrors.IsInvalid(err):
		return blocked("ResourceRejected", "Kubernetes rejected the generated resource or an immutable-field change"), err
	default:
		return pending("ResourceAPIError", "A Kubernetes resource operation failed and will be retried"), err
	}
}
