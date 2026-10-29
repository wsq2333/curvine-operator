package workload

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func cluster(t *testing.T) *api.CurvineCluster {
	t.Helper()
	data, err := os.ReadFile("../../examples/cluster-production.json")
	if err != nil {
		t.Fatal(err)
	}
	var c api.CurvineCluster
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	c.UID = "fixture-uid"
	c.Generation = 1
	return &c
}
func bundle(t *testing.T, c *api.CurvineCluster) *Bundle {
	t.Helper()
	b, err := Build(c, Contract)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func TestBuildPersistenceIdentityAndBootstrap(t *testing.T) {
	c := cluster(t)
	before := c.DeepCopy()
	b := bundle(t, c)
	if !reflect.DeepEqual(before, c) {
		t.Fatal("Build mutated input")
	}
	config := b.Objects[0].(*corev1.ConfigMap)
	for _, required := range []string{"format_master = false", "format_worker = false", "/var/lib/curvine/meta", "/var/lib/curvine/journal", "/var/lib/curvine/worker", masterDNS(c, 0), "id = 1", "id = 3"} {
		if !strings.Contains(config.Data["curvine-cluster.toml"], required) {
			t.Fatalf("missing runtime contract %q", required)
		}
	}
	if config.Immutable == nil || !*config.Immutable {
		t.Fatal("config must be immutable")
	}
	for _, s := range append([]*appsv1.StatefulSet{b.Master}, b.Workers...) {
		if !owned(c, s) || s.Spec.UpdateStrategy.Type != appsv1.OnDeleteStatefulSetStrategyType || s.Spec.PodManagementPolicy != appsv1.ParallelPodManagement {
			t.Fatal("unsafe workload lifecycle")
		}
		retention := s.Spec.PersistentVolumeClaimRetentionPolicy
		if retention == nil || retention.WhenDeleted != appsv1.RetainPersistentVolumeClaimRetentionPolicyType || retention.WhenScaled != appsv1.RetainPersistentVolumeClaimRetentionPolicyType {
			t.Fatal("claims can be deleted")
		}
		claim := s.Spec.VolumeClaimTemplates[0]
		if len(claim.OwnerReferences) != 0 || claim.Labels[UIDLabel] != string(c.UID) {
			t.Fatal("unsafe claim ownership")
		}
		p := s.Spec.Template.Spec
		if p.HostNetwork || p.SetHostnameAsFQDN != nil || *p.AutomountServiceAccountToken || !*p.SecurityContext.RunAsNonRoot {
			t.Fatal("unsafe pod defaults")
		}
		ct := p.Containers[0]
		if ct.Command[0] != "/app/curvine/lib/curvine-server" || ct.Args[1] != s.Labels[RoleLabel] || !strings.Contains(ct.Env[1].Value, s.Name+"."+c.Namespace+".svc.") {
			t.Fatal("wrong launch/DNS contract")
		}
		for _, v := range p.Volumes {
			if v.HostPath != nil {
				t.Fatal("host disk exposure")
			}
		}
	}
	if b.Master.Spec.Template.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution[0].TopologyKey != "kubernetes.io/hostname" {
		t.Fatal("master quorum placement unprotected")
	}
	svc := b.Objects[1].(*corev1.Service)
	if !svc.Spec.PublishNotReadyAddresses || svc.Spec.ClusterIP != "None" {
		t.Fatal("raft bootstrap DNS is gated on readiness")
	}
	pdb := b.Objects[2].(*policyv1.PodDisruptionBudget)
	if pdb.Spec.MinAvailable.IntVal != 2 {
		t.Fatal("quorum disruption budget")
	}
	ct := b.Master.Spec.Template.Spec.Containers[0]
	if ct.ReadinessProbe.TCPSocket.Port.IntVal != 8995 || ct.StartupProbe.TCPSocket.Port.IntVal != 8996 || ct.LivenessProbe.TCPSocket.Port.IntVal != 8996 {
		t.Fatal("snapshot recovery probes")
	}
}
func TestExpansionDoesNotReviseConfiguration(t *testing.T) {
	c := cluster(t)
	a := bundle(t, c)
	c.Spec.WorkerPools[0].Replicas++
	c.Spec.WorkerPools = append(c.Spec.WorkerPools, api.WorkerPool{Name: "extra", Replicas: 1, Storage: c.Spec.Master.Storage})
	b := bundle(t, c)
	if !reflect.DeepEqual(a.Master, b.Master) || !reflect.DeepEqual(a.Objects[0], b.Objects[0]) {
		t.Fatal("worker changes revise master or config")
	}
	for _, old := range a.Workers {
		for _, next := range b.Workers {
			if next.Name == old.Name && !reflect.DeepEqual(old.Spec.Template, next.Spec.Template) {
				t.Fatal("scale-out revises existing worker pods")
			}
		}
	}
	c.Spec.WorkerPools[0], c.Spec.WorkerPools[1] = c.Spec.WorkerPools[1], c.Spec.WorkerPools[0]
	if !reflect.DeepEqual(b, bundle(t, c)) {
		t.Fatal("pool ordering changes resources")
	}
	if _, err := Build(c, ""); err == nil {
		t.Fatal("implicit runtime contract")
	}
	c.UID = ""
	if _, err := Build(c, Contract); err == nil {
		t.Fatal("unpersisted intent executed")
	}
}
func TestOwnershipAndDangerousDrift(t *testing.T) {
	c := cluster(t)
	b := bundle(t, c)
	for _, test := range []struct {
		name, reason string
		edit         func(*appsv1.StatefulSet)
	}{
		{"foreign", "OwnershipConflict", func(s *appsv1.StatefulSet) { s.OwnerReferences[0].UID = "other" }},
		{"no-owner", "OwnershipConflict", func(s *appsv1.StatefulSet) { s.OwnerReferences = nil }},
		{"scale-in", "ScaleInBlocked", func(s *appsv1.StatefulSet) { s.Spec.Replicas = ptr.To(int32(5)) }},
		{"membership", "MembershipChangeBlocked", func(s *appsv1.StatefulSet) { s.Spec.Replicas = ptr.To(int32(1)) }},
		{"upgrade", "UpgradeRequired", func(s *appsv1.StatefulSet) { s.Spec.Template.Spec.Containers[0].Image = "other" }},
		{"storage", "StorageLayoutConflict", func(s *appsv1.StatefulSet) { s.Spec.VolumeClaimTemplates[0].Spec.StorageClassName = ptr.To("other") }},
		{"contract", "RuntimeContractConflict", func(s *appsv1.StatefulSet) { s.Annotations[ContractAnnotation] = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			live := b.Master.DeepCopy()
			test.edit(live)
			result := checkExisting(c, b.Master, live)
			if result == nil || result.Reason != test.reason {
				t.Fatalf("dangerous change allowed: %+v", result)
			}
		})
	}
}
func TestRetainedVolumeIdentity(t *testing.T) {
	c := cluster(t)
	b := bundle(t, c)
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	for _, foreign := range []bool{false, true} {
		claim := b.Master.Spec.VolumeClaimTemplates[0].DeepCopy()
		claim.Name = "data-" + b.Master.Name + "-0"
		claim.Namespace = c.Namespace
		if foreign {
			claim.Labels[UIDLabel] = "previous-cluster"
		}
		cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(claim).Build()
		e := Engine{Client: cl, Reader: cl, Contract: Contract}
		result, err := e.checkStorage(context.Background(), c, b)
		if err != nil {
			t.Fatal(err)
		}
		if foreign && (result == nil || result.Reason != "RetainedVolumeConflict") {
			t.Fatal("old volume adopted")
		}
		if !foreign && result != nil {
			t.Fatalf("current retained volume rejected: %+v", result)
		}
	}
}
func TestReadyRequiresCurrentRevision(t *testing.T) {
	s := bundle(t, cluster(t)).Master
	s.Generation = 4
	s.Status = appsv1.StatefulSetStatus{ObservedGeneration: 4, Replicas: 3, ReadyReplicas: 3, UpdatedReplicas: 3, CurrentRevision: "a", UpdateRevision: "a"}
	if !setReady(s, 3) {
		t.Fatal("current ready state rejected")
	}
	s.Status.UpdateRevision = "b"
	if setReady(s, 3) {
		t.Fatal("OnDelete pending rollout called ready")
	}
	s.Status.UpdateRevision = "a"
	s.Status.ObservedGeneration = 3
	if setReady(s, 3) {
		t.Fatal("stale workload status called ready")
	}
}
func TestWorkerDNSLength(t *testing.T) {
	c := cluster(t)
	c.Spec.WorkerPools[0].Name = strings.Repeat("w", 16)
	// Choose a domain accepted by the older master-only intent validation, but
	// too long for the largest worker ordinal and pool name.
	c.Name = strings.Repeat("a", 30)
	c.Namespace = strings.Repeat("b", 63)
	c.Spec.ClusterDomain = strings.Repeat("d", 60) + "." + strings.Repeat("e", 34)
	if c.Intent().Validate() != nil {
		t.Fatal("DNS fixture must pass master validation")
	}
	if _, err := Build(c, Contract); err == nil {
		t.Fatal("overlong worker DNS accepted")
	}
}
