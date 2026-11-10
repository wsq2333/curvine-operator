package expansion

import (
	"context"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/observe"
	"github.com/wsq2333/curvine-operator/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"testing"
	"time"
)

func TestProgressAndRestart(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	c := &api.CurvineCluster{ObjectMeta: metav1.ObjectMeta{Name: "cache", Namespace: "test", UID: "uid", Generation: 2}, Spec: api.Spec{WorkerPools: []api.WorkerPool{{Name: "ssd", Replicas: 1}}}}
	result := workload.Result{Status: metav1.ConditionFalse, Progressing: true, Reason: "WaitingForStatefulSets"}
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "cache-worker-ssd", Namespace: "test", UID: "sts", Generation: 1, OwnerReferences: []metav1.OwnerReference{{APIVersion: api.APIVersion, Kind: "CurvineCluster", Name: c.Name, UID: c.UID, Controller: ptr.To(true)}}}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(1))}, Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, UpdatedReplicas: 1, CurrentRevision: "rev", UpdateRevision: "rev"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: sts.Name + "-0", Namespace: "test", Labels: map[string]string{workload.UIDLabel: "uid"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", UID: "sts", Controller: ptr.To(true)}}}}
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-" + pod.Name, Namespace: "test", Labels: map[string]string{workload.UIDLabel: "uid"}}}
	now := time.Now()
	sample := &observe.Snapshot{Workers: observe.Healthy, Application: &api.ApplicationObservation{WorkerPools: []api.WorkerPoolObservation{{Name: "ssd", Desired: 1, Registered: 1, Fresh: 1, VersionVerified: 1}}}}
	check := func(phase, reason string, objects ...client.Object) api.WorkerPoolProgress {
		t.Helper()
		r := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
		p := Collect(context.Background(), r, c, sample, result, now)[0]
		if p.Phase != phase || p.Reason != reason {
			t.Fatalf("got %+v, want %s/%s", p, phase, reason)
		}
		return p
	}
	check("WaitingForResources", "StatefulSetMissing")
	check("WaitingForVolumes", "VolumeNotBound", sts)
	check("WaitingForScheduling", "PodUnscheduled", sts, pod, claim)
	pod.Spec.NodeName = "node"
	check("WaitingForVolumes", "VolumeNotBound", sts, pod, claim)
	claim.Status.Phase = corev1.ClaimBound
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "must-not-be-copied-secret"}}}}
	p := check("WaitingForPods", "ImagePullFailed", sts, pod, claim)
	c.Status.WorkerPools = []api.WorkerPoolProgress{p}
	now = now.Add(11 * time.Minute)
	p = check("WaitingForPods", "ImagePullFailed", sts, pod, claim)
	if !p.Stalled || !p.LastProgressTime.Equal(&c.Status.WorkerPools[0].LastProgressTime) {
		t.Fatal("restart lost deadline")
	}
	pod.Status.ContainerStatuses[0].State.Waiting.Reason = "CrashLoopBackOff"
	p = check("WaitingForPods", "ContainerCrashLoop", sts, pod, claim)
	if !p.Stalled || !p.LastProgressTime.Equal(&c.Status.WorkerPools[0].LastProgressTime) {
		t.Fatal("diagnostic change incorrectly reset deadline")
	}
	pod.Status.Phase = corev1.PodRunning
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	sample = nil
	p = check("WaitingForRegistration", "ApplicationEvidenceUnavailable", sts, pod, claim)
	if p.Registered != nil {
		t.Fatal("missing evidence became zero")
	}
	sample = &observe.Snapshot{Workers: observe.Healthy, Application: &api.ApplicationObservation{WorkerPools: []api.WorkerPoolObservation{{Name: "ssd", Desired: 1, Registered: 1, Fresh: 1, VersionVerified: 1}}}}
	p = check("Registered", "WorkerRegistrationVerified", sts, pod, claim)
	if p.Stalled {
		t.Fatal("progress did not reset stall")
	}
	pod.OwnerReferences[0].UID = "old-sts"
	check("Blocked", "PodIdentityConflict", sts, pod, claim)
	result = workload.Result{Status: metav1.ConditionFalse, Reason: "OwnershipConflict"}
	check("Blocked", "OwnershipConflict", sts, pod, claim)
}
