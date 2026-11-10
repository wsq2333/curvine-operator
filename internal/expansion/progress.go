// Package expansion explains allocation barriers without performing mutations.
package expansion

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/observe"
	"github.com/wsq2333/curvine-operator/internal/workload"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const StallAfter = 10 * time.Minute

// Collect uses uncached inventory. The caller must fence the final status write
// with the parent UID/generation after these reads.
func Collect(ctx context.Context, r client.Reader, c *api.CurvineCluster, s *observe.Snapshot, result workload.Result, now time.Time) []api.WorkerPoolProgress {
	var pods corev1.PodList
	var claims corev1.PersistentVolumeClaimList
	var sets appsv1.StatefulSetList
	failed := r.List(ctx, &pods, client.InNamespace(c.Namespace)) != nil
	failed = r.List(ctx, &claims, client.InNamespace(c.Namespace)) != nil || failed
	failed = r.List(ctx, &sets, client.InNamespace(c.Namespace)) != nil || failed
	pm := map[string]corev1.Pod{}
	cm := map[string]corev1.PersistentVolumeClaim{}
	sm := map[string]appsv1.StatefulSet{}
	for _, p := range pods.Items {
		pm[p.Name] = p
	}
	for _, p := range claims.Items {
		cm[p.Name] = p
	}
	for _, p := range sets.Items {
		sm[p.Name] = p
	}
	old := map[string]api.WorkerPoolProgress{}
	for _, p := range c.Status.WorkerPools {
		old[p.Name] = p
	}
	pools := append([]api.WorkerPool(nil), c.Spec.WorkerPools...)
	sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
	out := make([]api.WorkerPoolProgress, 0, len(pools))
	for _, pool := range pools {
		p := api.WorkerPoolProgress{Name: pool.Name, ObservedGeneration: c.Generation, Desired: int32(pool.Replicas), Phase: "WaitingForResources", Reason: "StatefulSetMissing"}
		name := c.Name + "-worker-" + pool.Name
		sts, exists := sm[name]
		owner := metav1.GetControllerOf(&sts)
		if failed {
			p.Phase = "Unknown"
			p.Reason = "InventoryUnavailable"
		} else if exists && (owner == nil || owner.UID != c.UID || owner.Kind != "CurvineCluster" || owner.APIVersion != api.APIVersion || owner.Name != c.Name || !sts.DeletionTimestamp.IsZero()) {
			p.Phase = "Blocked"
			p.Reason = "StatefulSetIdentityConflict"
			p.Resource = name
		} else if exists {
			p.Phase = "WaitingForRegistration"
			p.Reason = "ApplicationEvidenceUnavailable"
			var missingVolume, unscheduled, unready, identity string
			podReason := "PodNotReady"
			for i := 0; i < pool.Replicas; i++ {
				podName := fmt.Sprintf("%s-%d", name, i)
				claimName := "data-" + podName
				claim, ok := cm[claimName]
				if ok && claim.Labels[workload.UIDLabel] == string(c.UID) && len(claim.OwnerReferences) == 0 && claim.DeletionTimestamp.IsZero() && claim.Status.Phase == corev1.ClaimBound {
					p.VolumesBound++
				} else if missingVolume == "" {
					missingVolume = claimName
				}
				pod, ok := pm[podName]
				if !ok {
					if unready == "" {
						unready = podName
						podReason = "PodNotCreated"
					}
					continue
				}
				po := metav1.GetControllerOf(&pod)
				if po == nil || po.UID != sts.UID || po.Kind != "StatefulSet" || po.APIVersion != "apps/v1" || pod.Labels[workload.UIDLabel] != string(c.UID) {
					identity = podName
					continue
				}
				p.Created++
				if pod.Spec.NodeName != "" {
					p.Scheduled++
				} else if unscheduled == "" {
					unscheduled = podName
				}
				ready := false
				for _, cond := range pod.Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
						ready = true
					}
				}
				if ready && pod.DeletionTimestamp.IsZero() && pod.Status.Phase == corev1.PodRunning {
					p.Ready++
				} else if unready == "" {
					unready = podName
					for _, cs := range append(pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses...) {
						if cs.State.Waiting != nil {
							switch cs.State.Waiting.Reason {
							case "ImagePullBackOff", "ErrImagePull":
								podReason = "ImagePullFailed"
							case "CrashLoopBackOff":
								podReason = "ContainerCrashLoop"
							case "CreateContainerConfigError":
								podReason = "ContainerConfigurationFailed"
							}
						}
					}
				}
			}
			if s != nil && s.Application != nil {
				for _, a := range s.Application.WorkerPools {
					if a.Name == pool.Name && a.Desired == p.Desired {
						v := a.Registered
						p.Registered = &v
						if a.Registered == p.Desired && a.Fresh == p.Desired && a.VersionVerified == p.Desired && s.Workers == observe.Healthy {
							p.Phase = "Registered"
							p.Reason = "WorkerRegistrationVerified"
						} else {
							p.Reason = "WorkerRegistrationIncomplete"
							if a.Registered == p.Desired {
								switch {
								case a.Fresh != p.Desired:
									p.Reason = "WorkerHeartbeatStale"
								case a.VersionVerified != p.Desired:
									p.Reason = "WorkerVersionUnverified"
								default:
									p.Reason = "ApplicationHealthUnverified"
								}
							}
						}
					}
				}
			}
			switch {
			case identity != "":
				p.Phase = "Blocked"
				p.Reason = "PodIdentityConflict"
				p.Resource = identity
			case unscheduled != "":
				p.Phase = "WaitingForScheduling"
				p.Reason = "PodUnscheduled"
				p.Resource = unscheduled // WFFC volumes can legitimately wait for scheduling.
			case missingVolume != "":
				p.Phase = "WaitingForVolumes"
				p.Reason = "VolumeNotBound"
				p.Resource = missingVolume
			case unready != "":
				p.Phase = "WaitingForPods"
				p.Reason = podReason
				p.Resource = unready
			case sts.Spec.Replicas == nil || *sts.Spec.Replicas != p.Desired || sts.Status.ObservedGeneration < sts.Generation || sts.Status.CurrentRevision == "" || sts.Status.CurrentRevision != sts.Status.UpdateRevision || sts.Status.UpdatedReplicas != p.Desired:
				p.Phase = "WaitingForResources"
				p.Reason = "StatefulSetNotConverged"
				p.Resource = name
			}
		} else {
			p.Resource = name
		}
		// A resource-engine denial takes precedence over speculative Pod diagnosis.
		if result.Status == metav1.ConditionFalse && !result.Progressing {
			p.Phase = "Blocked"
			p.Reason = result.Reason
			p.Resource = ""
		}
		prior, ok := old[p.Name]
		p.LastProgressTime = metav1.NewTime(now)
		if ok {
			a, b := p, prior
			a.LastProgressTime = metav1.Time{}
			b.LastProgressTime = metav1.Time{}
			a.Stalled = false
			b.Stalled = false
			// Diagnostic wording/reason changes are not actual allocation progress.
			a.Phase = ""
			b.Phase = ""
			a.Reason = ""
			b.Reason = ""
			a.Resource = ""
			b.Resource = ""
			if reflect.DeepEqual(a, b) && !prior.LastProgressTime.IsZero() && !prior.LastProgressTime.After(now) {
				p.LastProgressTime = prior.LastProgressTime
			}
		}
		p.Stalled = p.Phase != "Registered" && now.Sub(p.LastProgressTime.Time) >= StallAfter
		out = append(out, p)
	}
	return out
}
