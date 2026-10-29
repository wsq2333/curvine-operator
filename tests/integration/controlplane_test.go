//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/controller"
	"github.com/wsq2333/curvine-operator/internal/observe"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controllerconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/yaml"
)

type switchObserver struct{ healthy atomic.Bool }

func (o *switchObserver) Observe(_ context.Context, target observe.Target) (observe.Snapshot, error) {
	if !o.healthy.Load() {
		return observe.Snapshot{}, observe.ErrUnsupported
	}
	return observe.Snapshot{ClusterUID: target.UID, Generation: target.Generation, ObservedAt: time.Now(), Version: "envtest-fixture", Quorum: observe.Healthy, Workers: observe.Healthy, DataAvailability: observe.Healthy}, nil
}

func eventually(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition did not converge within 15 seconds")
}

func TestControlPlane(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	assets := os.Getenv("KUBEBUILDER_ASSETS")
	download := os.Getenv("ENVTEST_DOWNLOAD") == "1"
	if assets == "" && !download {
		t.Fatal("set KUBEBUILDER_ASSETS or ENVTEST_DOWNLOAD=1; integration tests never silently skip")
	}
	if assets == "" {
		assets = filepath.Join(root, ".local", "envtest")
	}
	useExisting := false // Never use the caller's real cluster, even via environment.
	environment := &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join(root, "config/crd/bases")}, ErrorIfCRDPathMissing: true,
		BinaryAssetsDirectory: assets, DownloadBinaryAssets: download, DownloadBinaryAssetsVersion: "1.36.0", UseExistingCluster: &useExisting,
	}
	cfg, err := environment.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := environment.Stop(); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme, rbacv1.AddToScheme, coordinationv1.AddToScheme, policyv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	admin, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const namespace = "operator-test"
	for _, name := range []string{namespace, "outside-test"} {
		if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "examples/cluster-production.json"))
	if err != nil {
		t.Fatal(err)
	}
	var intent api.Cluster
	if err := json.Unmarshal(data, &intent); err != nil {
		t.Fatal(err)
	}
	makeCluster := func(name string) *api.CurvineCluster {
		return &api.CurvineCluster{TypeMeta: metav1.TypeMeta{APIVersion: api.APIVersion, Kind: "CurvineCluster"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: *intent.Spec.DeepCopy()}
	}

	t.Run("admission-defaults-and-rejections", func(t *testing.T) {
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}
		raw["metadata"] = map[string]any{"name": "defaults", "namespace": namespace}
		spec := raw["spec"].(map[string]any)
		for _, key := range []string{"clusterDomain", "profile", "deletionPolicy"} {
			delete(spec, key)
		}
		obj := &unstructured.Unstructured{Object: raw}
		if err := admin.Create(ctx, obj); err != nil {
			t.Fatal(err)
		}
		var got api.CurvineCluster
		if err := admin.Get(ctx, client.ObjectKeyFromObject(obj), &got); err != nil {
			t.Fatal(err)
		}
		if got.Spec.ClusterDomain != "cluster.local" || got.Spec.Profile != "development" || got.Spec.DeletionPolicy != "Retain" {
			t.Fatalf("defaults not applied: %+v", got.Spec)
		}
		for name, edit := range map[string]func(*api.CurvineCluster){
			"single-master":  func(c *api.CurvineCluster) { c.Spec.Master.Replicas = 1 },
			"even-master":    func(c *api.CurvineCluster) { c.Spec.Master.Replicas = 2 },
			"duplicate-pool": func(c *api.CurvineCluster) { c.Spec.WorkerPools = append(c.Spec.WorkerPools, c.Spec.WorkerPools[0]) },
			"bad-storage":    func(c *api.CurvineCluster) { c.Spec.Master.Storage.StorageClassName = "invalid/name" },
			"bad-domain":     func(c *api.CurvineCluster) { c.Spec.ClusterDomain = "bad..domain" },
			"delete-data":    func(c *api.CurvineCluster) { c.Spec.DeletionPolicy = "Delete" },
		} {
			c := makeCluster(name)
			edit(c)
			if err := admin.Create(ctx, c); !apierrors.IsInvalid(err) {
				t.Errorf("%s must be rejected, got %v", name, err)
			}
		}
	})

	t.Run("transition-rules-and-status-isolation", func(t *testing.T) {
		c := makeCluster("transitions")
		c.Status.ObservedGeneration = 99
		if err := admin.Create(ctx, c); err != nil {
			t.Fatal(err)
		}
		if c.Status.ObservedGeneration != 0 {
			t.Fatal("create accepted user status")
		}
		for name, edit := range map[string]func(*api.CurvineCluster){
			"identity": func(c *api.CurvineCluster) { c.Spec.ClusterID = "other" },
			"master":   func(c *api.CurvineCluster) { c.Spec.Master.Replicas = 5 },
			"image": func(c *api.CurvineCluster) {
				c.Spec.Image = "other@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			},
			"scale-in":     func(c *api.CurvineCluster) { c.Spec.WorkerPools[0].Replicas = 2 },
			"storage":      func(c *api.CurvineCluster) { c.Spec.WorkerPools[0].Storage.SizeGiB++ },
			"replace-pool": func(c *api.CurvineCluster) { c.Spec.WorkerPools[0].Name = "replacement" },
		} {
			changed := c.DeepCopy()
			edit(changed)
			if err := admin.Update(ctx, changed); !apierrors.IsInvalid(err) {
				t.Errorf("%s must be rejected: %v", name, err)
			}
		}
		c.Spec.WorkerPools[0].Replicas++
		if err := admin.Update(ctx, c); err != nil {
			t.Fatal(err)
		}
		if c.Generation != 2 {
			t.Fatalf("spec did not advance generation: %d", c.Generation)
		}
		c.Status.ObservedGeneration = 2
		if err := admin.Status().Update(ctx, c); err != nil {
			t.Fatal(err)
		}
		stale := c.DeepCopy()
		c.Status.ObservedGeneration = 3
		if err := admin.Status().Update(ctx, c); err != nil {
			t.Fatal(err)
		}
		if err := admin.Status().Update(ctx, stale); !apierrors.IsConflict(err) {
			t.Fatalf("expected real status conflict, got %v", err)
		}
		c.Status.ObservedGeneration = 999
		if err := admin.Update(ctx, c); err != nil {
			t.Fatal(err)
		}
		if c.Status.ObservedGeneration != 3 || c.Generation != 2 {
			t.Fatal("main update changed status or status update changed generation")
		}
	})

	// Exercise the checked-in Role, using the same service-account identity as
	// the Deployment rather than testing the manager with cluster-admin rights.
	roleData, err := os.ReadFile(filepath.Join(root, "config/rbac/role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.Role
	if err := yaml.Unmarshal(roleData, &role); err != nil {
		t.Fatal(err)
	}
	role.Namespace = namespace
	if err := admin.Create(ctx, &role); err != nil {
		t.Fatal(err)
	}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "curvine-operator", Namespace: namespace}}
	if err := admin.Create(ctx, sa); err != nil {
		t.Fatal(err)
	}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "curvine-operator", Namespace: namespace}, RoleRef: rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: role.Name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: namespace}}}
	if err := admin.Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	limitedConfig := rest.CopyConfig(cfg)
	limitedConfig.Impersonate = rest.ImpersonationConfig{UserName: "system:serviceaccount:" + namespace + ":" + sa.Name, Groups: []string{"system:serviceaccounts", "system:serviceaccounts:" + namespace, "system:authenticated"}}
	limited, err := client.New(limitedConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return limited.List(ctx, &api.CurvineClusterList{}, client.InNamespace(namespace)) == nil })
	t.Run("least-privilege", func(t *testing.T) {
		if err := limited.Create(ctx, makeCluster("forbidden")); !apierrors.IsForbidden(err) {
			t.Fatalf("spec creation permitted: %v", err)
		}
		var existing api.CurvineCluster
		if err := limited.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "transitions"}, &existing); err != nil {
			t.Fatal(err)
		}
		existing.Spec.WorkerPools[0].Replicas++
		if err := limited.Update(ctx, &existing); !apierrors.IsForbidden(err) {
			t.Fatalf("spec update permitted: %v", err)
		}
		for _, obj := range []client.ObjectList{&corev1.SecretList{}} {
			if err := limited.List(ctx, obj, client.InNamespace(namespace)); !apierrors.IsForbidden(err) {
				t.Errorf("unexpected workload/secret access: %v", err)
			}
		}
		if err := limited.List(ctx, &api.CurvineClusterList{}, client.InNamespace("outside-test")); !apierrors.IsForbidden(err) {
			t.Fatalf("cross-namespace access: %v", err)
		}
	})

	t.Run("workload-reconciliation", func(t *testing.T) { testWorkloads(t, cfg, scheme, admin, &role, makeCluster) })

	observed := makeCluster("observed")
	if err := admin.Create(ctx, observed); err != nil {
		t.Fatal(err)
	}
	outside := makeCluster("outside")
	outside.Namespace = "outside-test"
	if err := admin.Create(ctx, outside); err != nil {
		t.Fatal(err)
	}
	mgr, err := ctrl.NewManager(limitedConfig, ctrl.Options{Controller: controllerconfig.Controller{SkipNameValidation: ptr.To(true)}, Scheme: scheme, Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}}, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0", LeaderElection: true, LeaderElectionNamespace: namespace, LeaderElectionID: "curvine-operator-test"})
	if err != nil {
		t.Fatal(err)
	}
	observer := &switchObserver{}
	r := &controller.ClusterReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Observer: observer, PollInterval: 100 * time.Millisecond}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	managerCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- mgr.Start(managerCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("manager failed to stop")
		}
	})
	readObserved := func() *api.CurvineCluster {
		c := &api.CurvineCluster{}
		if err := admin.Get(ctx, client.ObjectKeyFromObject(observed), c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	t.Run("watch-status-lease-and-no-workloads", func(t *testing.T) {
		eventually(t, func() bool {
			cond := meta.FindStatusCondition(readObserved().Status.Conditions, controller.Ready)
			return cond != nil && cond.Status == metav1.ConditionUnknown && cond.Reason == "UnsupportedCapability"
		})
		var lease coordinationv1.Lease
		if err := admin.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "curvine-operator-test"}, &lease); err != nil {
			t.Fatal(err)
		}
		if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
			t.Fatal("leader lease not held")
		}
		before := readObserved().ResourceVersion
		time.Sleep(350 * time.Millisecond)
		if readObserved().ResourceVersion != before {
			t.Fatal("unchanged unsupported status churns")
		}
		if err := admin.Get(ctx, client.ObjectKeyFromObject(outside), outside); err != nil {
			t.Fatal(err)
		}
		if len(outside.Status.Conditions) != 0 {
			t.Fatal("outside namespace was reconciled")
		}
		var workloads appsv1.StatefulSetList
		if err := admin.List(ctx, &workloads, client.InNamespace(namespace)); err != nil || len(workloads.Items) != 0 {
			t.Fatalf("unexpected workloads: %v %+v", err, workloads.Items)
		}
	})
	t.Run("observer-transition-and-new-generation", func(t *testing.T) {
		observer.healthy.Store(true)
		eventually(t, func() bool { return meta.IsStatusConditionTrue(readObserved().Status.Conditions, controller.Ready) })
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			c := readObserved()
			c.Spec.WorkerPools[0].Replicas++
			return admin.Update(ctx, c)
		}); err != nil {
			t.Fatal(err)
		}
		eventually(t, func() bool {
			got := readObserved()
			cond := meta.FindStatusCondition(got.Status.Conditions, controller.Ready)
			return got.Status.ObservedGeneration == 2 && cond != nil && cond.ObservedGeneration == 2 && cond.Status == metav1.ConditionTrue
		})
		observer.healthy.Store(false)
		eventually(t, func() bool {
			got := readObserved()
			cond := meta.FindStatusCondition(got.Status.Conditions, controller.Ready)
			return got.Status.Observation == nil && cond != nil && cond.Status == metav1.ConditionUnknown
		})
	})
}
