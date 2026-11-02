//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/curvine/v054"
	"github.com/wsq2333/curvine-operator/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func testReadCache(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme, admin client.Client, sourceRole *rbacv1.Role) {
	ctx := context.Background()
	const ns = "readcache-test"
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	role := sourceRole.DeepCopy()
	role.ObjectMeta = metav1.ObjectMeta{Name: "operator", Namespace: ns}
	must(admin.Create(ctx, role))
	must(admin.Create(ctx, &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: ns}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "operator", Namespace: ns}}}))
	lc := rest.CopyConfig(cfg)
	lc.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + ns + ":operator", Groups: []string{"system:authenticated", "system:serviceaccounts", "system:serviceaccounts:" + ns}}
	limited, err := client.New(lc, client.Options{Scheme: scheme})
	must(err)
	eventually(t, func() bool { return limited.List(ctx, &batchv1.JobList{}, client.InNamespace(ns)) == nil })
	data, err := os.ReadFile("../../examples/cluster-read-cache.json")
	must(err)
	var c api.CurvineCluster
	must(json.Unmarshal(data, &c))
	c.Namespace = ns
	must(admin.Create(ctx, &c))
	for name, edit := range map[string]func(*api.CurvineCluster){
		"disable":     func(c *api.CurvineCluster) { c.Spec.ReadCache = nil },
		"source":      func(c *api.CurvineCluster) { c.Spec.ReadCache.Source = "s3://other/source" },
		"credentials": func(c *api.CurvineCluster) { c.Spec.ReadCache.CredentialsSecret = "other" },
	} {
		changed := c.DeepCopy()
		edit(changed)
		if err := admin.Update(ctx, changed); !apierrors.IsInvalid(err) {
			t.Fatalf("%s migration accepted: %v", name, err)
		}
	}
	if err := limited.List(ctx, &corev1.SecretList{}, client.InNamespace(ns)); !apierrors.IsForbidden(err) {
		t.Fatal("manager can read credentials")
	}
	engine := workload.Engine{Client: limited, Reader: limited, Contract: v054.Contract}
	until := func(reason string) {
		t.Helper()
		for i := 0; i < 40; i++ {
			r, err := engine.Reconcile(ctx, &c, nil)
			must(err)
			if r.Reason == reason {
				return
			}
			if r.Reason != "ReconcilingResources" {
				t.Fatalf("want %s, got %+v", reason, r)
			}
		}
		t.Fatal("did not converge")
	}
	until("WaitingForMasters")
	var master appsv1.StatefulSet
	must(admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: c.Name + "-master"}, &master))
	master.Status = appsv1.StatefulSetStatus{ObservedGeneration: master.Generation, Replicas: *master.Spec.Replicas, ReadyReplicas: *master.Spec.Replicas, UpdatedReplicas: *master.Spec.Replicas, CurrentRevision: "fixture", UpdateRevision: "fixture"}
	must(admin.Status().Update(ctx, &master))
	until("WaitingForOriginMount")
	var sets appsv1.StatefulSetList
	must(admin.List(ctx, &sets, client.InNamespace(ns)))
	if len(sets.Items) != 1+len(c.Spec.WorkerPools) {
		t.Fatal("read-cache workers still require unavailable quorum evidence")
	}
	var ds appsv1.DaemonSet
	if err := admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: c.Name + "-fuse"}, &ds); !apierrors.IsNotFound(err) {
		t.Fatal("FUSE created before origin verification")
	}
	var job batchv1.Job
	must(admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: c.Name + "-mount"}, &job))
	job.Status.Succeeded = 1
	timestamp := metav1.Now()
	job.Status.StartTime = &timestamp
	job.Status.CompletionTime = &timestamp
	job.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue}, {Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	must(admin.Status().Update(ctx, &job))
	until("WaitingForStatefulSets")
	must(admin.Get(ctx, client.ObjectKey{Namespace: ns, Name: c.Name + "-fuse"}, &ds))
	if ds.Spec.UpdateStrategy.Type != appsv1.OnDeleteDaemonSetStrategyType {
		t.Fatal("automatic client restart")
	}
	// A completed registration is immutable and preserved across expansion.
	rv := job.ResourceVersion
	c.Spec.WorkerPools[0].Replicas++
	must(admin.Update(ctx, &c))
	until("WaitingForStatefulSets")
	must(admin.Get(ctx, client.ObjectKeyFromObject(&job), &job))
	if job.ResourceVersion != rv {
		t.Fatal("expansion rewrote mount operation")
	}
	// CRD continues to reject scale-in even for rebuildable caches.
	reduced := c.DeepCopy()
	reduced.Spec.WorkerPools[0].Replicas--
	if err := admin.Update(ctx, reduced); !apierrors.IsInvalid(err) {
		t.Fatal("cache scale-in bypasses operation workflow")
	}
}
