//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/controller"
	"github.com/wsq2333/curvine-operator/internal/curvine/v054"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Real HTTP decoding -> controller -> real API status schema and restricted
// Role. Curvine responses are fixtures, not a running data plane.
func testReleaseContract(t *testing.T, cfg *rest.Config, scheme *runtime.Scheme, admin client.Client, sourceRole *rbacv1.Role, makeCluster func(string) *api.CurvineCluster) {
	ctx := context.Background()
	const ns = "release-contract-test"
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
		t.Fatal(err)
	}
	role := sourceRole.DeepCopy()
	role.ObjectMeta = metav1.ObjectMeta{Name: "release-observer", Namespace: ns}
	if err := admin.Create(ctx, role); err != nil {
		t.Fatal(err)
	}
	user := "system:serviceaccount:" + ns + ":release-observer"
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: role.Name, Namespace: ns}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: role.Name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: role.Name, Namespace: ns}}}
	if err := admin.Create(ctx, binding); err != nil {
		t.Fatal(err)
	}
	limitedConfig := rest.CopyConfig(cfg)
	limitedConfig.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:authenticated", "system:serviceaccounts", "system:serviceaccounts:" + ns}}
	limited, err := client.New(limitedConfig, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return limited.List(ctx, &api.CurvineClusterList{}, client.InNamespace(ns)) == nil })
	c := makeCluster("release")
	c.Namespace = ns
	if err := admin.Create(ctx, c); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			t.Error("non-read request")
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case v054.OverviewPath:
			host, _, _ := net.SplitHostPort(r.Host)
			state := "Standby"
			if strings.HasPrefix(host, "release-master-0.") {
				state = "Active"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"cluster_id": c.Spec.ClusterID, "master_addr": host + ":8995", "master_state": state, "live_workers": 0, "lost_workers": 0})
		case v054.WorkersPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"live_workers": []any{}, "lost_workers": []any{}})
		default:
			t.Error("unexpected HTTP path")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	defer transport.CloseIdleConnections()
	r := &controller.ClusterReconciler{Client: limited, APIReader: limited, Observer: v054.New(transport), WorkloadContract: v054.Contract}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)}
	for i := 0; i < 12; i++ {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if err := admin.Get(ctx, req.NamespacedName, c); err != nil {
		t.Fatal(err)
	}
	observation := c.Status.Observation
	if observation == nil || observation.Application == nil || observation.Application.SourceCommit != v054.SourceCommit || observation.Version != "" || observation.RegisteredWorkers == nil || *observation.RegisteredWorkers != 0 {
		t.Fatalf("status schema lost release evidence: %+v", observation)
	}
	for typ, reason := range map[string]string{controller.QuorumHealthy: "NoQuorumProofEndpoint", controller.DataAvailable: "NoDataReadProofEndpoint"} {
		cond := meta.FindStatusCondition(c.Status.Conditions, typ)
		if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != reason {
			t.Fatalf("wrong capability condition: %+v", cond)
		}
	}
	resources := meta.FindStatusCondition(c.Status.Conditions, controller.ResourcesReady)
	if resources == nil || resources.Reason != "QuorumObservationUnsupported" {
		t.Fatalf("missing fail-closed gate: %+v", resources)
	}
	var sets appsv1.StatefulSetList
	if err := admin.List(ctx, &sets, client.InNamespace(ns)); err != nil {
		t.Fatal(err)
	}
	if len(sets.Items) != 1 || sets.Items[0].Name != "release-master" {
		t.Fatalf("unexpected Worker creation: %+v", sets.Items)
	}
	// Verify the admitted map-list diagnostics survive a spec generation change.
	c.Spec.WorkerPools[0].Replicas++
	if err := admin.Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := admin.Get(ctx, req.NamespacedName, c); err != nil {
		t.Fatal(err)
	}
	if c.Status.ObservedGeneration != c.Generation || c.Status.Observation.Application.WorkerPools[0].Desired != int32(c.Spec.WorkerPools[0].Replicas) {
		t.Fatal("stale generation published")
	}
	fail.Store(true)
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := admin.Get(ctx, req.NamespacedName, c); err != nil {
		t.Fatal(err)
	}
	if c.Status.Observation != nil {
		t.Fatal("failed HTTP retained stale application evidence")
	}
}
