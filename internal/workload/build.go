// Package workload implements the explicitly selected Curvine runtime contract.
// It does not infer compatibility from an arbitrary image tag or digest.
package workload

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	Contract           = "curvine-728d2a46"
	ClusterLabel       = "storage.curvine.io/cluster"
	UIDLabel           = "storage.curvine.io/cluster-uid"
	RoleLabel          = "storage.curvine.io/role"
	PoolLabel          = "storage.curvine.io/pool"
	ContractAnnotation = "storage.curvine.io/runtime-contract"
	ConfigAnnotation   = "storage.curvine.io/config-hash"
	FieldManager       = "curvine-operator"
)

type Bundle struct {
	Objects []client.Object
	Master  *appsv1.StatefulSet
	Workers []*appsv1.StatefulSet
}

// Build is pure. Configuration is independent of worker replica counts and pool
// order, so expansion cannot silently roll masters or existing workers.
func Build(c *api.CurvineCluster, contract string) (*Bundle, error) {
	if contract != Contract {
		return nil, fmt.Errorf("unsupported runtime contract")
	}
	if err := c.Intent().Validate(); err != nil {
		return nil, err
	}
	if c.UID == "" {
		return nil, fmt.Errorf("a persisted cluster UID is required")
	}
	config := configuration(c)
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(config)))
	cm := &corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metadata(c, c.Name+"-config-"+hash[:12], "config", ""),
		Immutable:  ptr.To(true), Data: map[string]string{"curvine-cluster.toml": config},
	}
	master := statefulSet(c, c.Name+"-master", "master", "", c.Spec.Master.Replicas, c.Spec.Master.Storage, cm.Name, hash)
	b := &Bundle{Master: master, Objects: []client.Object{cm, service(c, master), budget(c, master), master}}
	pools := append([]api.WorkerPool(nil), c.Spec.WorkerPools...)
	sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
	for _, pool := range pools {
		if len(fmt.Sprintf("%s-worker-%s-9999.%s-worker-%s.%s.svc.%s", c.Name, pool.Name, c.Name, pool.Name, c.Namespace, c.Spec.ClusterDomain)) > 253 {
			return nil, fmt.Errorf("generated worker DNS exceeds 253 characters")
		}
		sts := statefulSet(c, c.Name+"-worker-"+pool.Name, "worker", pool.Name, pool.Replicas, pool.Storage, cm.Name, hash)
		b.Workers = append(b.Workers, sts)
		b.Objects = append(b.Objects, service(c, sts), budget(c, sts), sts)
	}
	return b, nil
}

func labels(c *api.CurvineCluster, role, pool string) map[string]string {
	m := map[string]string{ClusterLabel: c.Name, UIDLabel: string(c.UID), RoleLabel: role, "app.kubernetes.io/managed-by": FieldManager}
	if pool != "" {
		m[PoolLabel] = pool
	}
	return m
}
func metadata(c *api.CurvineCluster, name, role, pool string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: c.Namespace, Labels: labels(c, role, pool),
		Annotations:     map[string]string{ContractAnnotation: Contract},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: api.APIVersion, Kind: "CurvineCluster", Name: c.Name, UID: c.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(false)}},
	}
}
func selector(sts *appsv1.StatefulSet) map[string]string { return sts.Spec.Selector.MatchLabels }
func service(c *api.CurvineCluster, sts *appsv1.StatefulSet) *corev1.Service {
	role, pool := sts.Labels[RoleLabel], sts.Labels[PoolLabel]
	ports := []corev1.ServicePort{{Name: "rpc", Port: 8997, TargetPort: intstr.FromString("rpc")}, {Name: "web", Port: 9000, TargetPort: intstr.FromString("web")}}
	if role == "master" {
		ports[0].Port = 8995
		ports = append(ports, corev1.ServicePort{Name: "raft", Port: 8996, TargetPort: intstr.FromString("raft")})
	}
	return &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metadata(c, sts.Name, role, pool),
		Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: corev1.ClusterIPNone, PublishNotReadyAddresses: true, Selector: selector(sts), Ports: ports}}
}
func budget(c *api.CurvineCluster, sts *appsv1.StatefulSet) *policyv1.PodDisruptionBudget {
	// Worker data replication is not yet observable: voluntary evictions are
	// denied rather than guessing that one unavailable worker is safe.
	min := intstr.FromInt32(*sts.Spec.Replicas)
	if sts.Labels[RoleLabel] == "master" {
		min = intstr.FromInt32(*sts.Spec.Replicas/2 + 1)
	}
	return &policyv1.PodDisruptionBudget{TypeMeta: metav1.TypeMeta{APIVersion: "policy/v1", Kind: "PodDisruptionBudget"},
		ObjectMeta: metadata(c, sts.Name, sts.Labels[RoleLabel], sts.Labels[PoolLabel]), Spec: policyv1.PodDisruptionBudgetSpec{MinAvailable: &min, Selector: &metav1.LabelSelector{MatchLabels: selector(sts)}}}
}
func probe(port int32, start bool) *corev1.Probe {
	p := &corev1.Probe{ProbeHandler: corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(port)}}, PeriodSeconds: 10, TimeoutSeconds: 2, FailureThreshold: 6}
	if start {
		p.FailureThreshold = 180
	}
	return p
}
func statefulSet(c *api.CurvineCluster, name, role, pool string, replicas int, storage api.Storage, config, hash string) *appsv1.StatefulSet {
	sel := map[string]string{UIDLabel: string(c.UID), RoleLabel: role}
	if pool != "" {
		sel[PoolLabel] = pool
	}
	rpc, live := int32(8997), int32(8997)
	envName := "CURVINE_WORKER_HOSTNAME"
	if role == "master" {
		rpc, live = 8995, 8996
		envName = "CURVINE_MASTER_HOSTNAME"
	}
	ports := []corev1.ContainerPort{{Name: "rpc", ContainerPort: rpc}, {Name: "web", ContainerPort: 9000}}
	if role == "master" {
		ports = append(ports, corev1.ContainerPort{Name: "raft", ContainerPort: 8996})
	}
	// Explicit environment expansion gives the full DNS name without using
	// setHostnameAsFQDN, whose Linux hostname length limit is only 64 bytes.
	hostname := "$(POD_NAME)." + name + "." + c.Namespace + ".svc." + c.Spec.ClusterDomain
	container := corev1.Container{Name: "curvine", Image: c.Spec.Image, ImagePullPolicy: corev1.PullIfNotPresent,
		Command: []string{"/app/curvine/lib/curvine-server"}, Args: []string{"--service", role, "--conf", "/etc/curvine/curvine-cluster.toml"},
		Env:   []corev1.EnvVar{{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}}, {Name: envName, Value: hostname}},
		Ports: ports, ReadinessProbe: probe(rpc, false), StartupProbe: probe(live, true), LivenessProbe: probe(live, false),
		SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
		Resources:       corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("256Mi")}},
		VolumeMounts:    []corev1.VolumeMount{{Name: "config", MountPath: "/etc/curvine", ReadOnly: true}, {Name: "data", MountPath: "/var/lib/curvine"}, {Name: "logs", MountPath: "/var/log/curvine"}, {Name: "tmp", MountPath: "/tmp"}},
	}
	pod := corev1.PodSpec{AutomountServiceAccountToken: ptr.To(false), TerminationGracePeriodSeconds: ptr.To(int64(60)),
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(1000)), RunAsGroup: ptr.To(int64(1000)), FSGroup: ptr.To(int64(1000)), FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		Containers:      []corev1.Container{container}, Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: config}}}}, {Name: "logs", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}, {Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}}
	if role == "master" && replicas > 1 {
		pod.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{LabelSelector: &metav1.LabelSelector{MatchLabels: sel}, TopologyKey: "kubernetes.io/hostname"}}}}
		pod.TopologySpreadConstraints = []corev1.TopologySpreadConstraint{{MaxSkew: 1, TopologyKey: "topology.kubernetes.io/zone", WhenUnsatisfiable: corev1.DoNotSchedule, LabelSelector: &metav1.LabelSelector{MatchLabels: sel}}}
	}
	return &appsv1.StatefulSet{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "StatefulSet"}, ObjectMeta: metadata(c, name, role, pool), Spec: appsv1.StatefulSetSpec{
		ServiceName: name, Replicas: ptr.To(int32(replicas)), Selector: &metav1.LabelSelector{MatchLabels: sel}, PodManagementPolicy: appsv1.ParallelPodManagement,
		UpdateStrategy:                       appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType},
		PersistentVolumeClaimRetentionPolicy: &appsv1.StatefulSetPersistentVolumeClaimRetentionPolicy{WhenDeleted: appsv1.RetainPersistentVolumeClaimRetentionPolicyType, WhenScaled: appsv1.RetainPersistentVolumeClaimRetentionPolicyType},
		Template:                             corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(c, role, pool), Annotations: map[string]string{ConfigAnnotation: hash, ContractAnnotation: Contract}}, Spec: pod},
		VolumeClaimTemplates:                 []corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data", Labels: labels(c, role, pool)}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, StorageClassName: ptr.To(storage.StorageClassName), Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(fmt.Sprintf("%dGi", storage.SizeGiB))}}}}},
	}}
}
func configuration(c *api.CurvineCluster) string {
	var b strings.Builder
	fmt.Fprintf(&b, "format_master = false\nformat_worker = false\ncluster_id = %q\nnet_interface = \"\"\n\n[master]\nrpc_port = 8995\nweb_port = 9000\nmeta_dir = \"/var/lib/curvine/meta\"\nlog = { level = \"info\", log_dir = \"/var/log/curvine\", file_name = \"master.log\" }\naudit_log = { level = \"info\", log_dir = \"/var/log/curvine\", file_name = \"audit.log\" }\n\n[journal]\nenable = true\ngroup_name = %q\nrpc_port = 8996\njournal_dir = \"/var/lib/curvine/journal\"\njournal_addrs = [\n", c.Spec.ClusterID, c.Spec.ClusterID)
	for i := 0; i < c.Spec.Master.Replicas; i++ {
		fmt.Fprintf(&b, "  { id = %d, hostname = %q, port = 8996 },\n", i+1, masterDNS(c, i))
	}
	b.WriteString("]\n\n[worker]\nrpc_port = 8997\nweb_port = 9000\ndir_reserved = \"0\"\nfree_ratio = 0.1\ndata_dir = [\"/var/lib/curvine/worker\"]\nlog = { level = \"info\", log_dir = \"/var/log/curvine\", file_name = \"worker.log\" }\n\n[client]\nmaster_addrs = [\n")
	for i := 0; i < c.Spec.Master.Replicas; i++ {
		fmt.Fprintf(&b, "  { hostname = %q, port = 8995 },\n", masterDNS(c, i))
	}
	b.WriteString("]\n\n[log]\nlevel = \"info\"\nlog_dir = \"/var/log/curvine\"\nfile_name = \"client.log\"\n\n[discovery]\nenabled = false\n\n[mds]\nenabled = false\n\n[transfer]\nenabled = false\n")
	return b.String()
}
func masterDNS(c *api.CurvineCluster, i int) string {
	return fmt.Sprintf("%s-master-%d.%s-master.%s.svc.%s", c.Name, i, c.Name, c.Namespace, c.Spec.ClusterDomain)
}
