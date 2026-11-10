package telemetry

import (
	"context"
	"github.com/prometheus/client_golang/prometheus"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
	"time"
)

func TestMetricsExpireAndDisappear(t *testing.T) {
	now := time.Now()
	stamp := metav1.NewTime(now)
	c := &api.CurvineCluster{ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "ns", UID: "uid", Generation: 2}, Status: api.CurvineClusterStatus{ObservedGeneration: 2, AllocationSampleTime: &stamp, WorkerPools: []api.WorkerPoolProgress{{Name: "ssd", ObservedGeneration: 2, Desired: 3, Ready: 1, Phase: "WaitingForRegistration", Registered: ptr.To(int32(1))}}, Observation: &api.ObservationStatus{SampleTime: stamp, Application: &api.ApplicationObservation{WorkerPools: []api.WorkerPoolObservation{{Name: "ssd", Registered: 1}}}}}}
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(c).Build()
	registry := prometheus.NewRegistry()
	registry.MustRegister(&Collector{Reader: reader, Now: func() time.Time { return now }})
	names := func() map[string]bool {
		t.Helper()
		m, err := registry.Gather()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, f := range m {
			out[f.GetName()] = true
		}
		return out
	}
	if !names()["curvine_operator_worker_pool_registered"] || !names()["curvine_operator_worker_pool_replicas"] {
		t.Fatal("missing current evidence")
	}
	now = now.Add(2 * time.Minute)
	if names()["curvine_operator_worker_pool_registered"] || names()["curvine_operator_worker_pool_replicas"] {
		t.Fatal("stale metrics retained")
	}
	now = stamp.Time
	if err := reader.Delete(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if names()["curvine_operator_worker_pool_registered"] {
		t.Fatal("deleted labels leaked")
	}
}
