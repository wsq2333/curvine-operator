package cacheobserve

import (
	"context"
	"encoding/json"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/cachecontract"
	"github.com/wsq2333/curvine-operator/internal/observe"
	"github.com/wsq2333/curvine-operator/internal/workload"
	"io"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"net/http"
	"os"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCacheReportsAreScopedCurrentAndIndependent(t *testing.T) {
	data, err := os.ReadFile("../../examples/cluster-read-cache.json")
	if err != nil {
		t.Fatal(err)
	}
	var c api.CurvineCluster
	_ = json.Unmarshal(data, &c)
	c.UID = "cache-uid"
	c.Generation = 1
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	ds := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-fuse", Namespace: c.Namespace, UID: "ds-uid", Generation: 1, OwnerReferences: []metav1.OwnerReference{{APIVersion: api.APIVersion, Kind: "CurvineCluster", UID: c.UID, Controller: ptr.To(true)}}}, Status: appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 1}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "fuse-1", Namespace: c.Namespace, Labels: map[string]string{workload.UIDLabel: string(c.UID), workload.RoleLabel: "fuse"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "DaemonSet", UID: ds.UID, Controller: ptr.To(true)}}}, Spec: corev1.PodSpec{NodeName: "node-1"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "192.0.2.2"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(ds, pod).Build()
	conf := cachecontract.For(&c)
	for _, mode := range []string{"healthy", "origin-down", "no-hit", "stale", "wrong-config", "wrong-uid"} {
		t.Run(mode, func(t *testing.T) {
			report := cachecontract.NodeReport{ClusterUID: string(c.UID), ConfigHash: conf.ConfigHash, SampleTime: time.Now(), MountVerified: true, ReadVerified: true, OriginVerified: true, CacheHitObserved: true}
			switch mode {
			case "origin-down":
				report.OriginVerified = false
			case "no-hit":
				report.CacheHitObserved = false
			case "stale":
				report.SampleTime = time.Now().Add(-time.Hour)
			case "wrong-config":
				report.ConfigHash = "other"
			case "wrong-uid":
				report.ClusterUID = "other"
			}
			tr := transportFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "192.0.2.2:9101" || r.URL.Path != "/report" {
					t.Fatal("wrong endpoint")
				}
				body, _ := json.Marshal(report)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			got := Collect(context.Background(), cl, &c, tr)
			switch mode {
			case "stale", "wrong-config", "wrong-uid":
				if got.Read != observe.Unknown {
					t.Fatal("unbound report accepted")
				}
			case "origin-down":
				if got.Read != observe.Healthy || got.Origin != observe.Unhealthy {
					t.Fatal("origin outage hid cached read")
				}
			case "no-hit":
				if got.Read != observe.Healthy || got.Cache != observe.Unknown {
					t.Fatal("successful read fabricated cache hit")
				}
			default:
				if got.Read != observe.Healthy || got.Cache != observe.Healthy || got.Nodes != 1 {
					t.Fatalf("evidence lost: %+v", got)
				}
			}
		})
	}
}
