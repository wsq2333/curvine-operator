package controller

import (
	"context"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
)

func TestExpansionNeverClaimsDataOrReleaseHealth(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	c := &api.CurvineWorkerExpansion{ObjectMeta: metav1.ObjectMeta{Name: "expand", Namespace: "cache", UID: "uid", Generation: 1}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(c).WithObjects(c).Build()
	r := &WorkerExpansionReconciler{Client: cl, APIReader: cl}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(c)}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Get(context.Background(), client.ObjectKeyFromObject(c), c); err != nil {
		t.Fatal(err)
	}
	if c.Status.Phase != "Blocked" || c.Status.Reason != "SpecInvalid" {
		t.Fatal(c.Status)
	}
	for _, name := range []string{"DataPathVerified", "ReleaseProvenanceVerified"} {
		condition := meta.FindStatusCondition(c.Status.Conditions, name)
		if condition == nil || condition.Status != metav1.ConditionUnknown {
			t.Fatal("unproven health became known", name)
		}
	}
}
