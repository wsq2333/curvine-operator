package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/observe"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type observerFunc func(context.Context, observe.Target) (observe.Snapshot, error)

func (f observerFunc) Observe(ctx context.Context, target observe.Target) (observe.Snapshot, error) {
	return f(ctx, target)
}

type statusClient struct {
	client.Client
	patches  int
	conflict bool
}

func (c *statusClient) Status() client.SubResourceWriter {
	return &statusWriter{SubResourceWriter: c.Client.Status(), owner: c}
}

type statusWriter struct {
	client.SubResourceWriter
	owner *statusClient
}

func (w *statusWriter) Patch(ctx context.Context, obj client.Object, p client.Patch, opts ...client.SubResourcePatchOption) error {
	w.owner.patches++
	if w.owner.conflict {
		w.owner.conflict = false
		return apierrors.NewConflict(schema.GroupResource{Group: api.GroupVersion.Group, Resource: "curvineclusters"}, obj.GetName(), errors.New("concurrent write"))
	}
	return w.SubResourceWriter.Patch(ctx, obj, p, opts...)
}

func setup(t *testing.T) (*ClusterReconciler, *statusClient, ctrl.Request) {
	t.Helper()
	f, err := os.Open("../../examples/cluster-production.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	intent, err := api.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	c := &api.CurvineCluster{ObjectMeta: metav1.ObjectMeta{Name: intent.Metadata.Name, Namespace: intent.Metadata.Namespace, UID: types.UID("uid-1"), Generation: 1}, Spec: intent.Spec}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cl := &statusClient{Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(c).WithObjects(c).Build()}
	now := time.Now().UTC().Truncate(time.Second)
	r := &ClusterReconciler{Client: cl, APIReader: cl, Now: func() time.Time { return now }}
	return r, cl, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)}
}

func current(t *testing.T, cl client.Client, req ctrl.Request) *api.CurvineCluster {
	t.Helper()
	c := &api.CurvineCluster{}
	if err := cl.Get(context.Background(), req.NamespacedName, c); err != nil {
		t.Fatal(err)
	}
	return c
}

func reconcile(t *testing.T, r *ClusterReconciler, req ctrl.Request) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func healthy(now time.Time) observerFunc {
	return func(_ context.Context, t observe.Target) (observe.Snapshot, error) {
		n := int32(3)
		return observe.Snapshot{ClusterUID: t.UID, Generation: t.Generation, ObservedAt: now, Version: "fixture-only", Quorum: observe.Healthy, Workers: observe.Healthy, DataAvailability: observe.Healthy, RegisteredWorkers: &n}, nil
	}
}

func TestUnsupportedIsUnknownAndIdempotent(t *testing.T) {
	r, cl, req := setup(t)
	reconcile(t, r, req)
	c := current(t, cl, req)
	cond := meta.FindStatusCondition(c.Status.Conditions, Ready)
	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != "UnsupportedCapability" || c.Status.Observation != nil {
		t.Fatalf("false health: %+v", c.Status)
	}
	if c.Status.ObservedGeneration != 1 {
		t.Fatal("generation not recorded")
	}
	reconcile(t, r, req)
	if cl.patches != 1 {
		t.Fatalf("unchanged status patched %d times", cl.patches)
	}
}

func TestHealthAndFailureTransitions(t *testing.T) {
	r, cl, req := setup(t)
	r.Observer = healthy(r.Now())
	reconcile(t, r, req)
	if !meta.IsStatusConditionTrue(current(t, cl, req).Status.Conditions, Ready) {
		t.Fatal("healthy evidence not reflected")
	}
	r.Observer = observerFunc(func(context.Context, observe.Target) (observe.Snapshot, error) {
		return observe.Snapshot{}, errors.New("secret-token-must-not-leak")
	})
	reconcile(t, r, req)
	c := current(t, cl, req)
	if meta.FindStatusCondition(c.Status.Conditions, Ready).Status != metav1.ConditionUnknown || c.Status.Observation != nil || c.Status.LastSuccessfulObservationTime == nil {
		t.Fatalf("stale health retained: %+v", c.Status)
	}
	data, _ := json.Marshal(c.Status)
	if strings.Contains(string(data), "secret-token") {
		t.Fatal("raw error leaked")
	}
	for _, state := range []observe.Health{observe.Unknown, observe.Unhealthy} {
		r.Observer = observerFunc(func(ctx context.Context, target observe.Target) (observe.Snapshot, error) {
			s, _ := healthy(r.Now())(ctx, target)
			s.DataAvailability = state
			return s, nil
		})
		reconcile(t, r, req)
		want := metav1.ConditionUnknown
		if state == observe.Unhealthy {
			want = metav1.ConditionFalse
		}
		if meta.FindStatusCondition(current(t, cl, req).Status.Conditions, Ready).Status != want {
			t.Fatal("Ready ignored data availability")
		}
	}
}

func TestInvalidStaleAndTimedOutObservations(t *testing.T) {
	for _, test := range []struct{ name, reason string }{{"stale", "InvalidObservation"}, {"wrong-uid", "InvalidObservation"}, {"timeout", "ObservationTimeout"}} {
		t.Run(test.name, func(t *testing.T) {
			r, cl, req := setup(t)
			r.ObservationTimeout = time.Millisecond
			r.Observer = observerFunc(func(ctx context.Context, target observe.Target) (observe.Snapshot, error) {
				s, _ := healthy(r.Now())(ctx, target)
				switch test.name {
				case "stale":
					s.ObservedAt = s.ObservedAt.Add(-time.Hour)
				case "wrong-uid":
					s.ClusterUID = "other"
				case "timeout":
					<-ctx.Done()
					return s, ctx.Err()
				}
				return s, nil
			})
			reconcile(t, r, req)
			cond := meta.FindStatusCondition(current(t, cl, req).Status.Conditions, Ready)
			if cond.Status != metav1.ConditionUnknown || cond.Reason != test.reason {
				t.Fatalf("unexpected condition %+v", cond)
			}
		})
	}
}

func TestGenerationRaceAndStatusConflictDoNotPublishOldEvidence(t *testing.T) {
	r, cl, req := setup(t)
	r.Observer = observerFunc(func(ctx context.Context, target observe.Target) (observe.Snapshot, error) {
		c := current(t, cl, req)
		c.Generation++
		c.Spec.WorkerPools[0].Replicas++
		if err := cl.Update(ctx, c); err != nil {
			t.Fatal(err)
		}
		return healthy(r.Now())(ctx, target)
	})
	if result := reconcile(t, r, req); result.RequeueAfter != time.Second || cl.patches != 0 {
		t.Fatal("stale generation was published")
	}
	r.Observer = healthy(r.Now())
	cl.conflict = true
	if result := reconcile(t, r, req); result.RequeueAfter != time.Second {
		t.Fatal("conflict not requeued")
	}
	if len(current(t, cl, req).Status.Conditions) != 0 {
		t.Fatal("conflicting status persisted")
	}
	reconcile(t, r, req)
	if current(t, cl, req).Status.ObservedGeneration != 2 {
		t.Fatal("new generation not observed")
	}
}

func TestInvalidSpecDoesNotCallObserverAndPreservesOtherConditions(t *testing.T) {
	r, cl, req := setup(t)
	c := current(t, cl, req)
	c.Spec.Master.Replicas = 2
	if err := cl.Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	c.Status.Conditions = []metav1.Condition{{Type: "ExternalCheck", Status: metav1.ConditionTrue, Reason: "External", Message: "owned elsewhere", LastTransitionTime: metav1.Now()}}
	if err := cl.Status().Update(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	r.Observer = observerFunc(func(context.Context, observe.Target) (observe.Snapshot, error) {
		t.Fatal("invalid spec observed")
		return observe.Snapshot{}, nil
	})
	reconcile(t, r, req)
	c = current(t, cl, req)
	if !meta.IsStatusConditionFalse(c.Status.Conditions, SpecValid) || !meta.IsStatusConditionTrue(c.Status.Conditions, "ExternalCheck") {
		t.Fatalf("incorrect conditions: %+v", c.Status)
	}
}
