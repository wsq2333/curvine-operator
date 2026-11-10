package main

import (
	"encoding/json"
	"flag"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOfflinePlanContainsPinsNotConfiguration(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, v any) string {
		t.Helper()
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err = os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	master := appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "master", Namespace: "cache", UID: "master-uid"}}
	worker := appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "cache", UID: "worker-uid"}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/mnt/cache1", Type: ptr.To(corev1.HostPathDirectory)}}}}}}}}
	cm := corev1.ConfigMapList{Items: []corev1.ConfigMap{{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "cache", UID: "config-uid"}, Data: map[string]string{"sensitive": "never-print-this"}}}}
	node := corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-two", UID: "node-uid"}}
	oldArgs, oldFlags, oldStdout := os.Args, flag.CommandLine, os.Stdout
	t.Cleanup(func() { os.Args = oldArgs; flag.CommandLine = oldFlags; os.Stdout = oldStdout })
	output, err := os.CreateTemp(dir, "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	os.Stdout = output
	os.Args = []string{"expansion-plan", "--master", write("master", master), "--worker", write("worker", worker), "--configmaps", write("configs", cm), "--node", write("node", node), "--name", "expand", "--cluster-id", "cache", "--image", "example/curvine@sha256:" + strings.Repeat("a", 64)}
	flag.CommandLine = flag.NewFlagSet("plan", flag.ContinueOnError)
	if err = run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "never-print-this") {
		t.Fatal("configuration leaked")
	}
	var plan api.CurvineWorkerExpansion
	if err = json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Spec.Replicas != 1 || plan.Spec.Binding.Worker.UID != "worker-uid" || len(plan.Spec.Binding.ConfigMaps[0].SHA256) != 64 {
		t.Fatal("invalid preflight plan")
	}
}
