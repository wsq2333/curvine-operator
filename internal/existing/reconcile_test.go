package existing

import (
	"context"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/curvine/v054"
	appsv1 "k8s.io/api/apps/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"strings"
	"testing"
	"time"
)

type sampleObserver struct {
	sample v054.ImportedReport
	hook   func()
}

func (o *sampleObserver) ObserveImported(context.Context, string, string, string, string, string, time.Time) (v054.ImportedReport, error) {
	if o.hook != nil {
		o.hook()
	}
	return o.sample, nil
}
func fixture(t *testing.T) (*api.CurvineWorkerExpansion, Inputs) {
	t.Helper()
	image := "registry.example/curvine@sha256:" + strings.Repeat("a", 64)
	c := &api.CurvineWorkerExpansion{ObjectMeta: metav1.ObjectMeta{Name: "expand", Namespace: "test", UID: "cr", Generation: 1}}
	set := func(name string) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "test", UID: types.UID(name), Generation: 1}, Spec: appsv1.StatefulSetSpec{Replicas: ptr.To(int32(1)), ServiceName: name, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}}, Spec: corev1.PodSpec{HostNetwork: true, NodeSelector: map[string]string{"kubernetes.io/hostname": "old"}, Containers: []corev1.Container{{Name: "server", Image: "registry.example/curvine:old", Ports: []corev1.ContainerPort{{ContainerPort: 8997}, {ContainerPort: 19002}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}}, VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data/data1"}, {Name: "config", MountPath: "/etc/curvine"}}}}, InitContainers: []corev1.Container{{Name: "verify-nvme", Image: "registry.example/curvine:old", VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/data/data1"}}}}, Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/mnt/cache1", Type: ptr.To(corev1.HostPathDirectory)}}}, {Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "config"}}}}}}}}, Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 1, CurrentRevision: "rev", UpdateRevision: "rev"}}
	}
	m, w := set("master"), set("worker")
	pod := func(s *appsv1.StatefulSet, ip string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: s.Name + "-0", Namespace: s.Namespace, UID: types.UID(s.Name + "-pod"), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: s.Name, UID: s.UID, Controller: ptr.To(true)}}}, Spec: corev1.PodSpec{NodeName: "old", Containers: []corev1.Container{{Name: "server", Image: image}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, HostIP: ip, PodIP: ip, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "server", Ready: true, ImageID: image}}}}
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "test", UID: "cm"}, Data: map[string]string{"curvine.toml": "format_master = false\nformat_worker = false\ncluster_id = \"cache\"\n[worker]\nrpc_port = 8997\nweb_port = 19002\n[client]\nmaster_addrs = [{hostname = \"master-0.master.test.svc.cluster.local\", port = 8995}]\n"}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "new", UID: "node", Labels: map[string]string{"kubernetes.io/hostname": "new"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("64")}, Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "192.0.2.2"}}}}
	pin := func(s *appsv1.StatefulSet) api.PinnedObject {
		return api.PinnedObject{Name: s.Name, UID: string(s.UID), SHA256: Hash(s.Spec)}
	}
	c.Spec = api.WorkerExpansionSpec{Replicas: 2, Binding: api.WorkerExpansionBinding{Purpose: "ReadCache", ClusterID: "cache", Master: pin(m), Worker: pin(w), Image: image, TargetNode: "new", TargetNodeUID: "node", DataVolumes: []string{"data"}, ConfigMaps: []api.PinnedObject{{Name: "config", UID: "cm", SHA256: ConfigHash(cm)}}}}
	return c, Inputs{Master: m, Worker: w, MasterPod: pod(m, "192.0.2.1"), WorkerPod: pod(w, "192.0.2.1"), Node: node, ConfigMaps: map[string]*corev1.ConfigMap{"config": cm}}
}
func testClient(t *testing.T, c *api.CurvineWorkerExpansion, in Inputs) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{api.AddToScheme, appsv1.AddToScheme, corev1.AddToScheme, coordinationv1.AddToScheme} {
		if err := add(s); err != nil {
			t.Fatal(err)
		}
	}
	objects := []client.Object{c, in.Master, in.Worker, in.MasterPod, in.WorkerPod, in.Node}
	for _, cm := range in.ConfigMaps {
		objects = append(objects, cm)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objects...).WithStatusSubresource(&api.CurvineWorkerExpansion{}).Build()
}
func TestWorkerOnlyExpansionAndRestart(t *testing.T) {
	c, in := fixture(t)
	if why := Validate(c, in); why != "" {
		t.Fatal(why)
	}
	original := Hash(in.Worker.Spec)
	client := testClient(t, c, in)
	obs := &sampleObserver{sample: v054.ImportedReport{Registered: 1, ExistingHealthy: true}}
	engine := func() *Engine { return &Engine{Client: client, Reader: client, Observer: obs} }
	for _, reason := range []string{"ExpansionReserved", "ServiceCreated", "ConfigurationSnapshotCreated", "ConfigurationSnapshotCreated", "WorkerCreated", "WaitingForWorkerPod"} {
		out := engine().Reconcile(context.Background(), c)
		if out.Reason != reason {
			t.Fatalf("got %+v, expected %s", out, reason)
		}
	}
	var sts appsv1.StatefulSet
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: "expand-worker"}, &sts); err != nil {
		t.Fatal(err)
	}
	if len(sts.OwnerReferences) != 0 || sts.Spec.Template.Spec.NodeSelector["kubernetes.io/hostname"] != "new" || sts.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType {
		t.Fatal("unsafe lifecycle or placement")
	}
	p := in.WorkerPod.DeepCopy()
	p.Name = "expand-worker-0"
	p.Labels = map[string]string{BindingLabel: string(c.UID)}
	p.UID = "added"
	p.ResourceVersion = ""
	p.OwnerReferences[0].UID = sts.UID
	p.OwnerReferences[0].Name = sts.Name
	p.Spec.NodeName = "new"
	p.Status.HostIP = "192.0.2.2"
	p.Status.PodIP = "192.0.2.2"
	if err := client.Create(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	obs.sample = v054.ImportedReport{Registered: 2, ExistingHealthy: true, AddedHealthy: true}
	if out := engine().Reconcile(context.Background(), c); out.Phase != "Registered" {
		t.Fatal(out)
	}
	var unchanged appsv1.StatefulSet
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: "test", Name: "worker"}, &unchanged); err != nil {
		t.Fatal(err)
	}
	// Ignore fake-client resourceVersion bookkeeping when comparing original input.
	unchanged.ResourceVersion = in.Worker.ResourceVersion
	if Hash(unchanged.Spec) != original {
		t.Fatal("existing Worker was modified")
	}
	other := c.DeepCopy()
	other.Name = "competing"
	other.UID = "other"
	other.ResourceVersion = ""
	if err := client.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if out := engine().Reconcile(context.Background(), other); out.Reason != "ReservationConflict" {
		t.Fatal(out)
	}
}
func TestRejectUnsafeBindings(t *testing.T) {
	tests := []struct {
		name   string
		change func(*api.CurvineWorkerExpansion, Inputs)
		want   string
	}{
		{"same-node", func(c *api.CurvineWorkerExpansion, in Inputs) { in.WorkerPod.Spec.NodeName = "new" }, "TargetNodeUnavailable"},
		{"node-recreated", func(c *api.CurvineWorkerExpansion, in Inputs) { in.Node.UID = "recreated" }, "TargetNodeUnavailable"},
		{"cordoned", func(c *api.CurvineWorkerExpansion, in Inputs) { in.Node.Spec.Unschedulable = true }, "TargetNodeUnavailable"},
		{"taint", func(c *api.CurvineWorkerExpansion, in Inputs) {
			in.Node.Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "other", Effect: corev1.TaintEffectNoSchedule}}
		}, "TargetNodeUntolerated"},
		{"source-drift", func(c *api.CurvineWorkerExpansion, in Inputs) { in.Worker.Spec.Replicas = ptr.To(int32(2)) }, "SourceChanged"},
		{"config-drift", func(c *api.CurvineWorkerExpansion, in Inputs) { in.ConfigMaps["config"].Data["extra"] = "sensitive" }, "ConfigurationChanged"},
		{"image", func(c *api.CurvineWorkerExpansion, in Inputs) {
			in.WorkerPod.Status.ContainerStatuses[0].ImageID = "unknown"
		}, "RuntimeImageUnverified"},
		{"unbound-disk", func(c *api.CurvineWorkerExpansion, in Inputs) {
			in.Worker.Spec.Template.Spec.Volumes[0].HostPath.Type = ptr.To(corev1.HostPathDirectoryOrCreate)
			c.Spec.Binding.Worker.SHA256 = Hash(in.Worker.Spec)
		}, "UnsafeLocalStorage"},
		{"missing-guard", func(c *api.CurvineWorkerExpansion, in Inputs) {
			in.Worker.Spec.Template.Spec.InitContainers = nil
			c.Spec.Binding.Worker.SHA256 = Hash(in.Worker.Spec)
		}, "UnsupportedSourceLayout"},
		{"format", func(c *api.CurvineWorkerExpansion, in Inputs) {
			in.ConfigMaps["config"].Data["curvine.toml"] = strings.ReplaceAll(in.ConfigMaps["config"].Data["curvine.toml"], "format_worker = false", "format_worker = true")
			c.Spec.Binding.ConfigMaps[0].SHA256 = ConfigHash(in.ConfigMaps["config"])
		}, "UnsafeRuntimeConfiguration"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, in := fixture(t)
			tt.change(c, in)
			if got := Validate(c, in); got != tt.want {
				t.Fatalf("%s != %s", got, tt.want)
			}
		})
	}
}
func TestPreflightAndStaleIntentNeverCreateWorker(t *testing.T) {
	c, in := fixture(t)
	c.Spec.Replicas = 1
	cl := testClient(t, c, in)
	obs := &sampleObserver{sample: v054.ImportedReport{Registered: 1, ExistingHealthy: true}}
	e := &Engine{Client: cl, Reader: cl, Observer: obs}
	if out := e.Reconcile(context.Background(), c); out.Phase != "PreflightReady" {
		t.Fatal(out)
	}
	c.Spec.Replicas = 2
	if out := e.Reconcile(context.Background(), c); out.Reason != "IntentChanged" {
		t.Fatal(out)
	}
	var leases coordinationv1.LeaseList
	_ = cl.List(context.Background(), &leases)
	if len(leases.Items) != 0 {
		t.Fatal("preflight/stale request mutated lease")
	}
	c.Spec.Replicas = 1
	obs.hook = func() {
		cm := in.ConfigMaps["config"].DeepCopy()
		_ = cl.Get(context.Background(), client.ObjectKeyFromObject(cm), cm)
		cm.Data["extra"] = "changed"
		if err := cl.Update(context.Background(), cm); err != nil {
			t.Fatal(err)
		}
	}
	if out := e.Reconcile(context.Background(), c); out.Reason != "ConfigurationChanged" {
		t.Fatal(out)
	}
}
