//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/controller"
	"github.com/wsq2333/curvine-operator/internal/observe"
	"github.com/wsq2333/curvine-operator/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// This harness exercises real admission, SSA, RBAC and watches. It explicitly
// simulates StatefulSet/PVC status: envtest has no scheduler, kubelet, CSI or GC.
func testWorkloads(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme, admin client.Client, sourceRole *rbacv1.Role, makeCluster func(string) *api.CurvineCluster) {
	ctx := context.Background()
	const ns = "workload-test"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	role := sourceRole.DeepCopy()
	role.Namespace = ns
	role.ResourceVersion = ""
	role.UID = ""
	role.ManagedFields = nil
	must(admin.Create(ctx, role))
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "curvine-operator", Namespace: ns}}
	must(admin.Create(ctx, sa))
	must(admin.Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: sa.Name, Namespace: ns}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: role.Name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: ns}}}))
	limitedCfg := rest.CopyConfig(cfg)
	limitedCfg.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + ns + ":" + sa.Name, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + ns, "system:authenticated"}}
	limited, err := client.New(limitedCfg, client.Options{Scheme: scheme})
	must(err)
	eventually(t, func() bool { return limited.List(ctx, &appsv1.StatefulSetList{}, client.InNamespace(ns)) == nil })
	e := workload.Engine{Client: limited, Reader: limited, Contract: workload.Contract}
	create := func(name string) *api.CurvineCluster {
		c := makeCluster(name)
		c.Namespace = ns
		must(admin.Create(ctx, c))
		return c
	}
	build := func(c *api.CurvineCluster) *workload.Bundle {
		b, err := workload.Build(c, workload.Contract)
		must(err)
		return b
	}
	run := func(c *api.CurvineCluster, s *observe.Snapshot) workload.Result {
		r, err := e.Reconcile(ctx, c, s)
		must(err)
		return r
	}
	until := func(c *api.CurvineCluster, s *observe.Snapshot, reason string) {
		t.Helper()
		for i := 0; i < 40; i++ {
			r := run(c, s)
			if r.Reason == reason {
				return
			}
			if r.Reason != "ReconcilingResources" {
				t.Fatalf("want %s, got %+v", reason, r)
			}
		}
		t.Fatal("resource mutations did not converge")
	}
	ready := func(want *appsv1.StatefulSet) {
		var got appsv1.StatefulSet
		must(admin.Get(ctx, client.ObjectKeyFromObject(want), &got))
		got.Status = appsv1.StatefulSetStatus{ObservedGeneration: got.Generation, Replicas: *got.Spec.Replicas, ReadyReplicas: *got.Spec.Replicas, UpdatedReplicas: *got.Spec.Replicas, CurrentRevision: "fixture", UpdateRevision: "fixture"}
		must(admin.Status().Update(ctx, &got))
		for i := int32(0); i < *got.Spec.Replicas; i++ {
			p := got.Spec.VolumeClaimTemplates[0].DeepCopy()
			p.Name = fmt.Sprintf("data-%s-%d", got.Name, i)
			p.Namespace = ns
			err := admin.Create(ctx, p)
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			must(err)
			p.Status.Phase = corev1.ClaimBound
			must(admin.Status().Update(ctx, p))
		}
	}
	c := create("managed")
	b := build(c)
	until(c, nil, "WaitingForQuorum")
	var workers appsv1.StatefulSet
	if err := admin.Get(ctx, client.ObjectKeyFromObject(b.Workers[0]), &workers); !apierrors.IsNotFound(err) {
		t.Fatal("workers created without quorum")
	}
	ready(b.Master)
	if r := run(c, nil); r.Reason != "WaitingForQuorum" {
		t.Fatalf("Pod readiness substituted for quorum: %+v", r)
	}
	sample := &observe.Snapshot{ClusterUID: string(c.UID), Generation: c.Generation, ObservedAt: time.Now(), Version: "test-fixture", Quorum: observe.Healthy, Workers: observe.Healthy, DataAvailability: observe.Healthy}
	stale := *sample
	stale.ObservedAt = time.Now().Add(-time.Hour)
	if r := run(c, &stale); r.Reason != "WaitingForQuorum" {
		t.Fatal("stale health permitted worker creation")
	}
	stale = *sample
	stale.ClusterUID = "other"
	if r := run(c, &stale); r.Reason != "WaitingForQuorum" {
		t.Fatal("foreign health permitted worker creation")
	}

	until(c, sample, "WaitingForStatefulSets")
	for _, s := range b.Workers {
		ready(s)
	}
	until(c, sample, "ResourcesConverged")
	// No-op SSA dry runs must not write objects or endlessly bump revisions.
	versions := map[string]string{}
	for _, o := range b.Objects {
		must(admin.Get(ctx, client.ObjectKeyFromObject(o), o))
		versions[fmt.Sprintf("%T/%s", o, o.GetName())] = o.GetResourceVersion()
	}
	until(c, sample, "ResourcesConverged")
	for _, o := range b.Objects {
		must(admin.Get(ctx, client.ObjectKeyFromObject(o), o))
		if versions[fmt.Sprintf("%T/%s", o, o.GetName())] != o.GetResourceVersion() {
			t.Fatalf("no-op churn: %T/%s", o, o.GetName())
		}
	}
	// Repair only owned fields; preserve independently maintained labels.
	var service corev1.Service
	must(admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: b.Master.Name}, &service))
	service.Labels["user.example/keep"] = "yes"
	service.Spec.Ports[0].TargetPort = intstr.FromInt32(1234)
	must(admin.Update(ctx, &service))
	until(c, sample, "ResourcesConverged")
	must(admin.Get(ctx, client.ObjectKeyFromObject(&service), &service))
	if service.Labels["user.example/keep"] != "yes" || service.Spec.Ports[0].TargetPort.StrVal != "rpc" {
		t.Fatal("SSA drift repair overwrote user fields or failed")
	}
	// Worker expansion is gated on real health; it never changes the master.
	var master appsv1.StatefulSet
	must(admin.Get(ctx, client.ObjectKeyFromObject(b.Master), &master))
	masterGeneration := master.Generation
	c.Spec.WorkerPools[0].Replicas++
	must(admin.Update(ctx, c))
	sample.Generation = c.Generation
	sample.Workers = observe.Unknown
	until(c, sample, "WaitingForApplicationHealth")
	must(admin.Get(ctx, client.ObjectKeyFromObject(b.Workers[0]), &workers))
	if *workers.Spec.Replicas != *b.Workers[0].Spec.Replicas {
		t.Fatal("expanded with unknown worker health")
	}
	sample.Workers = observe.Healthy
	until(c, sample, "WaitingForStatefulSets")
	b = build(c)
	for _, s := range b.Workers {
		ready(s)
	}
	until(c, sample, "ResourcesConverged")
	must(admin.Get(ctx, client.ObjectKeyFromObject(b.Master), &master))
	if master.Generation != masterGeneration {
		t.Fatal("worker expansion revised masters")
	}
	// Controller restart is represented by a fresh engine with no in-memory state.
	restarted := workload.Engine{Client: limited, Reader: limited, Contract: workload.Contract}
	result, err := restarted.Reconcile(ctx, c, sample)
	must(err)
	if result.Reason != "ResourcesConverged" {
		t.Fatalf("restart lost progress: %+v", result)
	}
	// Existing workload excess must never be "repaired" with destructive scale-in.
	must(admin.Get(ctx, client.ObjectKeyFromObject(b.Workers[0]), &workers))
	*workers.Spec.Replicas++
	must(admin.Update(ctx, &workers))
	if r := run(c, sample); r.Reason != "ScaleInBlocked" {
		t.Fatalf("scale-in was not blocked: %+v", r)
	}
	// Restore manually changed fixture for subsequent checks (the operator cannot).
	*workers.Spec.Replicas--
	must(admin.Update(ctx, &workers))
	ready(b.Workers[0])
	// A late conflict anywhere in the bundle prevents earlier creates too.
	foreign := create("foreign")
	fb := build(foreign)
	conflict := fb.Workers[0].DeepCopy()
	conflict.OwnerReferences = nil
	must(admin.Create(ctx, conflict))
	if r := run(foreign, nil); r.Reason != "OwnershipConflict" {
		t.Fatalf("unowned workload adopted: %+v", r)
	}
	var config corev1.ConfigMap
	if err := admin.Get(ctx, client.ObjectKeyFromObject(fb.Objects[0]), &config); !apierrors.IsNotFound(err) {
		t.Fatal("partial writes before conflict detection")
	}
	// A parent change between preflight and mutation must invalidate the work.
	racing := create("racing")
	reader := &generationReader{Reader: limited, admin: admin, key: client.ObjectKeyFromObject(racing)}
	racingEngine := workload.Engine{Client: limited, Reader: reader, Contract: workload.Contract}
	result, err = racingEngine.Reconcile(ctx, racing, nil)
	if !apierrors.IsConflict(err) || result.Reason != "IntentChanged" {
		t.Fatalf("stale generation was executed: %+v %v", result, err)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(build(racing).Objects[0]), &config); !apierrors.IsNotFound(err) {
		t.Fatal("stale intent created resources")
	}
	// A competing same-name Create must not turn into SSA adoption.
	competing := create("competing")
	competingClient := &competingCreator{Client: limited, admin: admin}
	competingEngine := workload.Engine{Client: competingClient, Reader: limited, Contract: workload.Contract}
	result, err = competingEngine.Reconcile(ctx, competing, nil)
	if !apierrors.IsAlreadyExists(err) || result.Reason != "ResourceConflict" {
		t.Fatalf("competing object overwritten: %+v %v", result, err)
	}
	must(admin.Get(ctx, client.ObjectKeyFromObject(build(competing).Objects[0]), &config))
	if len(config.OwnerReferences) != 0 {
		t.Fatal("competing object adopted")
	}
	// Retained data from an older incarnation, including unlabeled claims, blocks bootstrap.
	retained := create("retained")
	rb := build(retained)
	pvc := rb.Master.Spec.VolumeClaimTemplates[0].DeepCopy()
	pvc.Name = "data-" + rb.Master.Name + "-0"
	pvc.Namespace = ns
	pvc.Labels = nil
	must(admin.Create(ctx, pvc))
	if r := run(retained, nil); r.Reason != "RetainedVolumeConflict" {
		t.Fatalf("retained PVC adopted: %+v", r)
	}
	if err := admin.Get(ctx, client.ObjectKeyFromObject(rb.Objects[0]), &config); !apierrors.IsNotFound(err) {
		t.Fatal("partial writes before retained PVC check")
	}
	// Least privilege: resource management does not authorize deleting data or pods.
	for _, o := range []client.Object{pvc, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: ns}}, b.Master} {
		if err := limited.Delete(ctx, o); !apierrors.IsForbidden(err) {
			t.Fatalf("unexpected delete privilege for %T: %v", o, err)
		}
	}
	if err := limited.Create(ctx, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "forbidden", Namespace: ns}}); !apierrors.IsForbidden(err) {
		t.Fatalf("unexpected PVC create privilege: %v", err)
	}
	// Deletion leaves claims untouched and a replacement CR cannot adopt them.
	must(admin.Delete(ctx, c))
	replacement := makeCluster(c.Name)
	replacement.Namespace = ns
	must(admin.Create(ctx, replacement))
	if r := run(replacement, nil); r.Reason != "OwnershipConflict" {
		t.Fatalf("replacement adopted old workloads: %+v", r)
	}
	// envtest has no garbage collector: explicitly simulate child GC, excluding claims.
	for _, o := range b.Objects {
		must(admin.Delete(ctx, o))
	}
	if r := run(replacement, nil); r.Reason != "RetainedVolumeConflict" {
		t.Fatalf("replacement adopted retained data after child GC: %+v", r)
	}
	var surviving corev1.PersistentVolumeClaim
	must(admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: "data-" + b.Master.Name + "-0"}, &surviving))
	if len(surviving.OwnerReferences) != 0 || surviving.Labels[workload.UIDLabel] != string(c.UID) {
		t.Fatal("retention identity lost")
	}
	// Exercise watches with a one-hour poll: child status and Service/PVC events
	// must enqueue the cluster, independent of timer-based observation.
	mgr, err := ctrl.NewManager(limitedCfg, ctrl.Options{Scheme: scheme, Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}}, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
	must(err)
	r := &controller.ClusterReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), WorkloadContract: workload.Contract, PollInterval: time.Hour}
	must(r.SetupWithManager(mgr))
	managerCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- mgr.Start(managerCtx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			must(err)
		case <-time.After(10 * time.Second):
			t.Fatal("workload manager did not stop")
		}
	}()
	watched := create("watched")
	wb := build(watched)
	eventually(t, func() bool { return admin.Get(ctx, client.ObjectKeyFromObject(wb.Master), &master) == nil })
	eventually(t, func() bool {
		var got api.CurvineCluster
		if admin.Get(ctx, client.ObjectKeyFromObject(watched), &got) != nil {
			return false
		}
		cond := meta.FindStatusCondition(got.Status.Conditions, controller.ResourcesReady)
		return cond != nil && cond.Reason == "WaitingForQuorum"
	})
	must(admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: wb.Master.Name}, &service))
	service.Spec.Ports[0].TargetPort = intstr.FromInt32(42)
	must(admin.Update(ctx, &service))
	eventually(t, func() bool {
		var s corev1.Service
		return admin.Get(ctx, client.ObjectKeyFromObject(&service), &s) == nil && s.Spec.Ports[0].TargetPort.StrVal == "rpc"
	})
	// PVC watch works although retained PVCs have no controller owner reference.
	p := wb.Master.Spec.VolumeClaimTemplates[0].DeepCopy()
	p.Name = "data-" + wb.Master.Name + "-0"
	p.Namespace = ns
	p.Labels[workload.UIDLabel] = "old-uid"
	must(admin.Create(ctx, p))
	eventually(t, func() bool {
		var got api.CurvineCluster
		if admin.Get(ctx, client.ObjectKeyFromObject(watched), &got) != nil {
			return false
		}
		cond := meta.FindStatusCondition(got.Status.Conditions, controller.ResourcesReady)
		return cond != nil && cond.Reason == "RetainedVolumeConflict"
	})
}

// Test-only race injection; no production hooks or simulated-health flags.
type generationReader struct {
	client.Reader
	admin client.Client
	key   client.ObjectKey
	reads int
}

func (r *generationReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*api.CurvineCluster); ok && key == r.key {
		r.reads++
		if r.reads == 2 {
			var latest api.CurvineCluster
			if err := r.admin.Get(ctx, key, &latest); err != nil {
				return err
			}
			latest.Spec.WorkerPools[0].Replicas++
			if err := r.admin.Update(ctx, &latest); err != nil {
				return err
			}
		}
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

type competingCreator struct {
	client.Client
	admin    client.Client
	injected bool
}

func (c *competingCreator) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	options := (&client.CreateOptions{}).ApplyOptions(opts)
	if !c.injected && len(options.DryRun) == 0 {
		c.injected = true
		foreign := obj.DeepCopyObject().(client.Object)
		foreign.SetOwnerReferences(nil)
		if err := c.admin.Create(ctx, foreign); err != nil {
			return err
		}
	}
	return c.Client.Create(ctx, obj, opts...)
}
