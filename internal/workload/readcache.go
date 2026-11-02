package workload

import (
	"encoding/json"
	"fmt"
	"reflect"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/cachecontract"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func cacheResources(c *api.CurvineCluster, b *Bundle, cm *corev1.ConfigMap) {
	conf := cachecontract.For(c)
	data, _ := json.Marshal(conf)
	cm.Data["cache.json"] = string(data)
	cm.Data["fuse.toml"] = cm.Data["curvine-cluster.toml"] + fmt.Sprintf("\n[fuse]\nfs_path = %q\nmnt_path = \"/var/lib/curvine-export/data\"\nreadonly = true\nfuse_opts = [\"ro\", \"allow_other\", \"nodev\", \"nosuid\"]\nmnt_number = 1\nio_threads = 2\nworker_threads = 2\ndirect_io = true\nweb_port = 9000\nstate_dir = \"/var/lib/curvine-state\"\nlog = { level = \"warn\", log_dir = \"/var/log/curvine\", file_name = \"fuse.log\" }\n", cachecontract.MountPath)
	// No credentials in either ConfigMap, arguments, environment or status.
	volumes := []corev1.Volume{
		{Name: "config", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cm.Name}, DefaultMode: ptr.To(int32(0444))}}},
		{Name: "origin", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: c.Spec.ReadCache.CredentialsSecret, DefaultMode: ptr.To(int32(0440)), Items: []corev1.KeyToPath{{Key: "accessKey", Path: "accessKey"}, {Key: "secretKey", Path: "secretKey"}}}}},
		{Name: "logs", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	mounts := []corev1.VolumeMount{{Name: "config", MountPath: "/etc/curvine", ReadOnly: true}, {Name: "origin", MountPath: "/etc/curvine-origin", ReadOnly: true}, {Name: "logs", MountPath: "/var/log/curvine"}, {Name: "tmp", MountPath: "/tmp"}}
	resources := corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("256Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("1Gi")}}
	jobContainer := corev1.Container{Name: "mount", Image: c.Spec.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/app/curvine/lib/curvine-cache-mount"}, Args: []string{"configure"}, VolumeMounts: mounts, Resources: resources, SecurityContext: &corev1.SecurityContext{ReadOnlyRootFilesystem: ptr.To(true), AllowPrivilegeEscalation: ptr.To(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}}
	b.MountJob = &batchv1.Job{ObjectMeta: metadata(c, c.Name+"-mount", "mount", ""), Spec: batchv1.JobSpec{BackoffLimit: ptr.To(int32(3)), ActiveDeadlineSeconds: ptr.To(int64(300)), Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(c, "mount", "")}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(1000)), RunAsGroup: ptr.To(int64(1000)), FSGroup: ptr.To(int64(1000))}, Containers: []corev1.Container{jobContainer}, Volumes: volumes}}}}
	b.MountJob.TypeMeta = metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"}
	selector := map[string]string{UIDLabel: string(c.UID), RoleLabel: "fuse"}
	fuseVolumes := append([]corev1.Volume(nil), volumes...)
	fuseVolumes = append(fuseVolumes, corev1.Volume{Name: "export", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: cachecontract.HostPath(c), Type: ptr.To(corev1.HostPathDirectoryOrCreate)}}}, corev1.Volume{Name: "fuse-device", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/dev/fuse", Type: ptr.To(corev1.HostPathCharDev)}}}, corev1.Volume{Name: "state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
	fuseMounts := append([]corev1.VolumeMount(nil), mounts...)
	fuseMounts = append(fuseMounts, corev1.VolumeMount{Name: "export", MountPath: "/var/lib/curvine-export", MountPropagation: ptr.To(corev1.MountPropagationBidirectional)}, corev1.VolumeMount{Name: "fuse-device", MountPath: "/dev/fuse"}, corev1.VolumeMount{Name: "state", MountPath: "/var/lib/curvine-state"})
	node := corev1.Container{Name: "fuse", Image: c.Spec.Image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/app/curvine/lib/curvine-cache-node"}, VolumeMounts: fuseMounts, Resources: resources, Ports: []corev1.ContainerPort{{Name: "probe", ContainerPort: cachecontract.ProbePort}, {Name: "metrics", ContainerPort: 9000}}, SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true), RunAsUser: ptr.To(int64(0)), ReadOnlyRootFilesystem: ptr.To(true)}, ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/readyz", Port: intstr.FromInt32(cachecontract.ProbePort)}}, PeriodSeconds: 10, TimeoutSeconds: 2, FailureThreshold: 2}}
	// FUSE needs /dev/fuse and mount propagation. Scope the privileged client to
	// explicitly opted-in Linux nodes and a UID-specific host directory.
	b.FUSE = &appsv1.DaemonSet{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "DaemonSet"}, ObjectMeta: metadata(c, c.Name+"-fuse", "fuse", ""), Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: selector}, UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.OnDeleteDaemonSetStrategyType}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels(c, "fuse", ""), Annotations: map[string]string{ConfigAnnotation: conf.ConfigHash}}, Spec: corev1.PodSpec{NodeSelector: map[string]string{"kubernetes.io/os": "linux", cachecontract.NodeLabel: "enabled"}, AutomountServiceAccountToken: ptr.To(false), TerminationGracePeriodSeconds: ptr.To(int64(60)), Containers: []corev1.Container{node}, Volumes: fuseVolumes}}}}
	b.Objects = append(b.Objects, b.MountJob, b.FUSE)
}
func jobComplete(o client.Object) bool {
	j, ok := o.(*batchv1.Job)
	if !ok {
		return false
	}
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobComplete && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
func jobFailed(o client.Object) bool {
	j, ok := o.(*batchv1.Job)
	if !ok {
		return false
	}
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
func fuseReady(o client.Object) bool {
	d, ok := o.(*appsv1.DaemonSet)
	return ok && d.Status.ObservedGeneration >= d.Generation && d.Status.DesiredNumberScheduled > 0 && d.Status.NumberReady == d.Status.DesiredNumberScheduled && d.Status.UpdatedNumberScheduled == d.Status.DesiredNumberScheduled
}

// Job pod templates cannot be updated; reject drift in the fields we own.
func jobMatches(want, got *batchv1.Job) bool {
	w, g := want.Spec.Template.Spec, got.Spec.Template.Spec
	if len(g.Containers) != 1 || len(g.InitContainers) != 0 || len(g.Volumes) != len(w.Volumes) || g.HostNetwork || g.HostPID || g.HostIPC {
		return false
	}
	if g.AutomountServiceAccountToken == nil || *g.AutomountServiceAccountToken || !reflect.DeepEqual(w.SecurityContext, g.SecurityContext) || g.ServiceAccountName != "default" && g.ServiceAccountName != "" {
		return false
	}
	wc, gc := w.Containers[0], g.Containers[0]
	return gc.Image == wc.Image && reflect.DeepEqual(gc.Command, wc.Command) && reflect.DeepEqual(gc.Args, wc.Args) && reflect.DeepEqual(gc.Env, wc.Env) && len(gc.EnvFrom) == 0 && reflect.DeepEqual(gc.VolumeMounts, wc.VolumeMounts) && reflect.DeepEqual(gc.SecurityContext, wc.SecurityContext) && reflect.DeepEqual(g.Volumes, w.Volumes) && g.RestartPolicy == w.RestartPolicy && reflect.DeepEqual(want.Spec.ActiveDeadlineSeconds, got.Spec.ActiveDeadlineSeconds) && reflect.DeepEqual(want.Spec.BackoffLimit, got.Spec.BackoffLimit)
}
