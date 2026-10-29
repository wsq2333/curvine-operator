package v1alpha1

import (
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

func TestKubernetesRegistrationAndIntentIsolation(t *testing.T) {
	intent := validCluster(t)
	n := int32(3)
	c := &CurvineCluster{
		TypeMeta:   metav1.TypeMeta{APIVersion: APIVersion, Kind: "CurvineCluster"},
		ObjectMeta: metav1.ObjectMeta{Name: intent.Metadata.Name, Namespace: intent.Metadata.Namespace, Labels: map[string]string{"owner": "original"}},
		Spec:       intent.Spec,
		Status:     CurvineClusterStatus{Observation: &ObservationStatus{Version: "fixture", SampleTime: metav1.Now(), RegisteredWorkers: &n}},
	}
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	obj, gvk, err := serializer.NewCodecFactory(scheme).UniversalDeserializer().Decode(data, nil, nil)
	if err != nil || gvk == nil || *gvk != GroupVersion.WithKind("CurvineCluster") {
		t.Fatalf("registration/serialization failed: %v %v", gvk, err)
	}
	if _, ok := obj.(*CurvineCluster); !ok {
		t.Fatalf("unexpected decoded type %T", obj)
	}
	copy := c.DeepCopy()
	copy.Labels["owner"] = "changed"
	copy.Spec.WorkerPools[0].Replicas++
	*copy.Status.Observation.RegisteredWorkers = 99
	if c.Labels["owner"] != "original" || c.Spec.WorkerPools[0].Replicas != 3 || *c.Status.Observation.RegisteredWorkers != 3 {
		t.Fatal("deep copy aliases resource data")
	}
	offline := c.Intent()
	if err := offline.Validate(); err != nil {
		t.Fatal(err)
	}
	offline.Spec.WorkerPools[0].Name = "changed"
	if c.Spec.WorkerPools[0].Name == "changed" {
		t.Fatal("offline intent aliases Kubernetes object")
	}
}
