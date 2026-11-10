// Package existing implements worker-only expansion without adopting Helm resources.
package existing

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"path"
	"reflect"
	"regexp"
	"strings"
)

//go:embed diskguard.py
var diskGuard string

const BindingLabel = "storage.curvine.io/expansion-uid"
const NameLabel = "storage.curvine.io/expansion"
const FingerprintAnnotation = "storage.curvine.io/expansion-binding"

// Hash canonicalizes through JSON maps so an offline planner and API defaults
// use the same hashing contract. It does not publish the hashed configuration.
func Hash(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var normalized any
	if err = json.Unmarshal(raw, &normalized); err != nil {
		panic(err)
	}
	raw, err = json.Marshal(normalized)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}
func ConfigHash(c *corev1.ConfigMap) string {
	return Hash(struct {
		Data   map[string]string `json:"data"`
		Binary map[string][]byte `json:"binaryData"`
	}{c.Data, c.BinaryData})
}
func pinned(p api.PinnedObject, obj metav1.Object, hash string) bool {
	return p.Name == obj.GetName() && p.UID == string(obj.GetUID()) && p.SHA256 == hash && obj.GetDeletionTimestamp() == nil
}

type Inputs struct {
	Master, Worker       *appsv1.StatefulSet
	MasterPod, WorkerPod *corev1.Pod
	Node                 *corev1.Node
	ConfigMaps           map[string]*corev1.ConfigMap
}

func readyPod(p *corev1.Pod, s *appsv1.StatefulSet) bool {
	owner := metav1.GetControllerOf(p)
	if owner == nil || owner.Kind != "StatefulSet" || owner.APIVersion != "apps/v1" || owner.UID != s.UID || p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning || p.Spec.NodeName == "" {
		return false
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
func imageMatches(p *corev1.Pod, image string) bool {
	if len(p.Spec.Containers) != 1 || len(p.Status.ContainerStatuses) != 1 {
		return false
	}
	id := p.Status.ContainerStatuses[0].ImageID
	id = strings.TrimPrefix(strings.TrimPrefix(id, "docker-pullable://"), "containerd://")
	return p.Status.ContainerStatuses[0].Ready && id == image
}
func readySet(s *appsv1.StatefulSet) bool {
	return s.DeletionTimestamp == nil && s.Spec.Replicas != nil && *s.Spec.Replicas == 1 && s.Status.ObservedGeneration == s.Generation && s.Status.ReadyReplicas == 1 && s.Status.CurrentRevision != "" && s.Status.CurrentRevision == s.Status.UpdateRevision
}

// Validate rejects layouts outside the narrow, audited import contract. Pins
// authorize copying a reviewed template; they do not authorize formatting disks.
func Validate(c *api.CurvineWorkerExpansion, in Inputs) string {
	if c.Spec.Validate() != nil || c.UID == "" || len(c.Name) > 40 {
		return "SpecInvalid"
	}
	b := c.Spec.Binding
	if !pinned(b.Master, in.Master, Hash(in.Master.Spec)) || !pinned(b.Worker, in.Worker, Hash(in.Worker.Spec)) {
		return "SourceChanged"
	}
	if !readySet(in.Master) || !readySet(in.Worker) || !readyPod(in.MasterPod, in.Master) || !readyPod(in.WorkerPod, in.Worker) {
		return "SourceNotReady"
	}
	if !imageMatches(in.MasterPod, b.Image) || !imageMatches(in.WorkerPod, b.Image) {
		return "RuntimeImageUnverified"
	}
	if in.Node.Name != b.TargetNode || in.Node.Labels["kubernetes.io/hostname"] != b.TargetNode || string(in.Node.UID) != b.TargetNodeUID || in.Node.DeletionTimestamp != nil || in.Node.Spec.Unschedulable || in.Node.Name == in.WorkerPod.Spec.NodeName || in.Node.Name == in.MasterPod.Spec.NodeName {
		return "TargetNodeUnavailable"
	}
	ready := false
	for _, condition := range in.Node.Status.Conditions {
		if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		return "TargetNodeNotReady"
	}
	p := in.Worker.Spec.Template.Spec
	for _, v := range p.Volumes {
		if v.Name == "operator-disk-guard" {
			return "ReservedVolumeName"
		}
	}
	if !p.HostNetwork || p.NodeName != "" || len(p.Containers) != 1 || len(p.InitContainers) != 1 || len(in.Worker.Spec.VolumeClaimTemplates) != 0 {
		return "UnsupportedSourceLayout"
	}
	if p.InitContainers[0].Image != p.Containers[0].Image {
		return "UnsupportedGuardImage"
	}
	ports := map[int32]bool{}
	for _, port := range p.Containers[0].Ports {
		ports[port.ContainerPort] = true
	}
	if !ports[8997] || !ports[19002] {
		return "UnsupportedWorkerPorts"
	}
	for _, t := range in.Node.Spec.Taints {
		if t.Effect != corev1.TaintEffectNoSchedule && t.Effect != corev1.TaintEffectNoExecute {
			continue
		}
		tolerated := false
		for _, v := range p.Tolerations {
			if (v.Effect == "" || v.Effect == t.Effect) && ((v.Operator == corev1.TolerationOpExists && (v.Key == "" || v.Key == t.Key)) || ((v.Operator == corev1.TolerationOpEqual || v.Operator == "") && v.Key == t.Key && v.Value == t.Value)) {
				tolerated = true
			}
		}
		if !tolerated {
			return "TargetNodeUntolerated"
		}
	}
	// Scheduler still decides whether currently unallocated resources suffice.
	for k, v := range p.Containers[0].Resources.Requests {
		a, ok := in.Node.Status.Allocatable[k]
		if !ok || a.Cmp(v) < 0 {
			return "TargetNodeTooSmall"
		}
	}
	for _, pin := range b.ConfigMaps {
		cm := in.ConfigMaps[pin.Name]
		if cm == nil || !pinned(pin, cm, ConfigHash(cm)) {
			return "ConfigurationChanged"
		}
	}
	// The imported configuration must explicitly preserve the existing cluster,
	// disable formatting, and reference its Master. Only the reviewed wire layout is supported.
	tomls := 0
	for _, cm := range in.ConfigMaps {
		for key, value := range cm.Data {
			if !strings.HasSuffix(key, ".toml") {
				continue
			}
			tomls++
			section := ""
			workerConfig := ""
			for _, line := range strings.Split(value, "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "[") {
					section = trimmed
				}
				if section == "[worker]" {
					workerConfig += line + "\n"
				}
			}
			for field, expectedPort := range map[string]string{"rpc_port": "8997", "web_port": "19002"} {
				if !regexp.MustCompile(`(?m)^\s*` + field + `\s*=\s*` + expectedPort + `\s*(?:#.*)?$`).MatchString(workerConfig) {
					return "UnsupportedWorkerPorts"
				}
			}
			for _, field := range []string{"format_master", "format_worker"} {
				if !regexp.MustCompile(`(?m)^\s*` + field + `\s*=\s*false\s*(?:#.*)?$`).MatchString(value) {
					return "UnsafeRuntimeConfiguration"
				}
			}
			if !regexp.MustCompile(`(?m)^\s*cluster_id\s*=\s*"` + regexp.QuoteMeta(b.ClusterID) + `"\s*(?:#.*)?$`).MatchString(value) {
				return "ClusterIdentityMismatch"
			}
			expected := in.Master.Name + "-0." + in.Master.Spec.ServiceName + "." + c.Namespace + ".svc.cluster.local"
			if !strings.Contains(value, `"`+expected+`"`) {
				return "MasterEndpointMismatch"
			}
		}
	}
	if tomls != 1 {
		return "UnsupportedRuntimeConfiguration"
	}
	data := map[string]bool{}
	for _, n := range b.DataVolumes {
		data[n] = true
	}
	found := map[string]bool{}
	configs := map[string]bool{}
	for _, v := range p.Volumes {
		switch {
		case v.ConfigMap != nil:
			if _, ok := in.ConfigMaps[v.ConfigMap.Name]; !ok {
				return "UnpinnedConfiguration"
			}
			configs[v.ConfigMap.Name] = true
		case v.HostPath != nil:
			if !data[v.Name] || v.HostPath.Type == nil || *v.HostPath.Type != corev1.HostPathDirectory || !strings.HasPrefix(v.HostPath.Path, "/mnt/") || path.Clean(v.HostPath.Path) != v.HostPath.Path {
				return "UnsafeLocalStorage"
			}
			found[v.Name] = true
		case v.EmptyDir != nil:
		default:
			return "UnsupportedVolume"
		}
	}
	if len(found) != len(data) || len(configs) != len(b.ConfigMaps) {
		return "UnpinnedConfiguration"
	}
	for _, container := range append([]corev1.Container{p.Containers[0]}, p.InitContainers...) {
		for _, env := range container.Env {
			if env.ValueFrom != nil && (env.ValueFrom.SecretKeyRef != nil || env.ValueFrom.ConfigMapKeyRef != nil) {
				return "UnsupportedEnvironmentReference"
			}
		}
		if len(container.EnvFrom) > 0 {
			return "UnsupportedEnvironmentReference"
		}
		mounted := map[string]bool{}
		for _, m := range container.VolumeMounts {
			if data[m.Name] {
				if m.SubPath != "" || m.SubPathExpr != "" || m.ReadOnly {
					return "UnsupportedDiskMount"
				}
				mounted[m.Name] = true
			}
		}
		if len(mounted) != len(data) {
			return "DiskGuardMissing"
		}
	}
	return ""
}

// Build copies the pinned source layout, retaining disk guards and resources.
// It uses separate names/selectors and no ownerReference: deletion of the
// expansion CR must not implicitly stop an active external-cluster Worker.
func Build(c *api.CurvineWorkerExpansion, source *appsv1.StatefulSet) (*appsv1.StatefulSet, *corev1.Service, *corev1.ConfigMap) {
	name := c.Name + "-worker"
	labels := map[string]string{BindingLabel: string(c.UID), NameLabel: c.Name}
	metadata := metav1.ObjectMeta{Name: name, Namespace: c.Namespace, Labels: labels, Annotations: map[string]string{FingerprintAnnotation: Hash(c.Spec.Binding)}}
	s := &appsv1.StatefulSet{ObjectMeta: metadata, Spec: *source.Spec.DeepCopy()}
	s.Spec.Replicas = ptr.To(int32(1))
	s.Spec.ServiceName = name
	s.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
	s.Spec.UpdateStrategy = appsv1.StatefulSetUpdateStrategy{Type: appsv1.OnDeleteStatefulSetStrategyType}
	s.Spec.Ordinals = nil
	s.Spec.Template.ObjectMeta = metav1.ObjectMeta{Labels: labels}
	p := &s.Spec.Template.Spec
	for i := range p.Volumes {
		if p.Volumes[i].ConfigMap != nil {
			for _, pin := range c.Spec.Binding.ConfigMaps {
				if p.Volumes[i].ConfigMap.Name == pin.Name {
					p.Volumes[i].ConfigMap.Name = configName(c, pin)
					break
				}
			}
		}
	}
	p.NodeSelector = map[string]string{"kubernetes.io/hostname": c.Spec.Binding.TargetNode}
	p.Hostname = ""
	p.Subdomain = ""
	p.SetHostnameAsFQDN = nil
	p.AutomountServiceAccountToken = ptr.To(false)
	p.Affinity = &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{LabelSelector: source.Spec.Selector.DeepCopy(), TopologyKey: "kubernetes.io/hostname"}}}}
	p.Containers[0].Image = c.Spec.Binding.Image
	guardName := c.Name + "-disk-guard"
	guard := &corev1.ConfigMap{ObjectMeta: *metadata.DeepCopy(), Immutable: ptr.To(true), Data: map[string]string{"guard.py": diskGuard}}
	guard.Name = guardName
	paths := []string{}
	mounts := []corev1.VolumeMount{}
	for _, m := range p.Containers[0].VolumeMounts {
		for _, name := range c.Spec.Binding.DataVolumes {
			if m.Name == name {
				paths = append(paths, m.MountPath)
				mounts = append(mounts, m)
			}
		}
	}
	encoded, _ := json.Marshal(paths)
	mounts = append(mounts, corev1.VolumeMount{Name: "operator-disk-guard", MountPath: "/operator-guard", ReadOnly: true})
	p.Volumes = append(p.Volumes, corev1.Volume{Name: "operator-disk-guard", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: guardName}, DefaultMode: ptr.To(int32(420))}}})
	identityGuard := corev1.Container{Name: "operator-disk-identity", Image: c.Spec.Binding.Image, ImagePullPolicy: p.Containers[0].ImagePullPolicy, Command: []string{"python3", "/operator-guard/guard.py", string(c.UID), c.Spec.Binding.ClusterID, string(encoded)}, VolumeMounts: mounts, SecurityContext: p.InitContainers[0].SecurityContext.DeepCopy(), TerminationMessagePath: "/dev/termination-log", TerminationMessagePolicy: corev1.TerminationMessageReadFile}
	p.InitContainers = append(p.InitContainers, identityGuard)
	for i := range p.InitContainers {
		p.InitContainers[i].Image = c.Spec.Binding.Image
	}
	env := []corev1.EnvVar{}
	for _, v := range p.Containers[0].Env {
		if v.Name != "CURVINE_WORKER_HOSTNAME" {
			env = append(env, v)
		}
	}
	p.Containers[0].Env = append(env, corev1.EnvVar{Name: "CURVINE_WORKER_HOSTNAME", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "status.hostIP"}}})
	svc := &corev1.Service{ObjectMeta: *metadata.DeepCopy(), Spec: corev1.ServiceSpec{ClusterIP: corev1.ClusterIPNone, Selector: labels, Ports: []corev1.ServicePort{{Name: "rpc", Port: 8997}}}}
	return s, svc, guard
}
func matches(c *api.CurvineWorkerExpansion, got, want *appsv1.StatefulSet) bool {
	return got.DeletionTimestamp == nil && len(got.OwnerReferences) == 0 && got.Labels[BindingLabel] == string(c.UID) && got.Annotations[FingerprintAnnotation] == Hash(c.Spec.Binding) && reflect.DeepEqual(got.Spec, want.Spec)
}

func configName(c *api.CurvineWorkerExpansion, pin api.PinnedObject) string {
	return c.Name + "-config-" + Hash(pin)[:12]
}
func configurationSnapshots(c *api.CurvineWorkerExpansion, in Inputs) []*corev1.ConfigMap {
	out := []*corev1.ConfigMap{}
	for _, pin := range c.Spec.Binding.ConfigMaps {
		original := in.ConfigMaps[pin.Name]
		cm := original.DeepCopy()
		cm.ObjectMeta = metav1.ObjectMeta{Name: configName(c, pin), Namespace: c.Namespace, Labels: map[string]string{BindingLabel: string(c.UID), NameLabel: c.Name}, Annotations: map[string]string{FingerprintAnnotation: Hash(c.Spec.Binding)}}
		cm.Immutable = ptr.To(true)
		out = append(out, cm)
	}
	return out
}
