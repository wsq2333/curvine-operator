// expansion-plan renders a preflight-only CR from exported Kubernetes JSON.
// It never loads kubeconfig, connects to a cluster, or applies resources.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/existing"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"os"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	master := flag.String("master", "", "exported single-Master StatefulSet JSON")
	worker := flag.String("worker", "", "exported single-Worker StatefulSet JSON")
	configs := flag.String("configmaps", "", "JSON ConfigMapList with exactly referenced configuration maps")
	node := flag.String("node", "", "exported target Node JSON")
	name := flag.String("name", "", "new expansion resource name")
	clusterID := flag.String("cluster-id", "", "existing read-cache cluster ID")
	image := flag.String("image", "", "approved exact runtime repository@sha256 digest")
	flag.Parse()
	read := func(path string, dst any) error {
		if path == "" {
			return fmt.Errorf("all input files are required")
		}
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("cannot open input file")
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil || stat.Size() > 4<<20 {
			return fmt.Errorf("input exceeds 4 MiB")
		}
		if err = json.NewDecoder(f).Decode(dst); err != nil {
			return fmt.Errorf("invalid input JSON")
		}
		return nil
	}
	var m, w appsv1.StatefulSet
	var cm corev1.ConfigMapList
	var n corev1.Node
	for _, input := range []struct {
		path string
		dst  any
	}{{*master, &m}, {*worker, &w}, {*configs, &cm}, {*node, &n}} {
		if err := read(input.path, input.dst); err != nil {
			return err
		}
	}
	if len(validation.IsDNS1035Label(*name)) > 0 || len(*name) > 40 || m.Namespace == "" || m.Namespace != w.Namespace {
		return fmt.Errorf("invalid name or source namespace")
	}
	pin := func(s *appsv1.StatefulSet) api.PinnedObject {
		return api.PinnedObject{Name: s.Name, UID: string(s.UID), SHA256: existing.Hash(s.Spec)}
	}
	b := api.WorkerExpansionBinding{Purpose: "ReadCache", ClusterID: *clusterID, Master: pin(&m), Worker: pin(&w), TargetNode: n.Name, TargetNodeUID: string(n.UID), Image: *image}
	for _, v := range cm.Items {
		if v.Namespace != m.Namespace {
			return fmt.Errorf("ConfigMap namespace mismatch")
		}
		b.ConfigMaps = append(b.ConfigMaps, api.PinnedObject{Name: v.Name, UID: string(v.UID), SHA256: existing.ConfigHash(&v)})
	}
	for _, v := range w.Spec.Template.Spec.Volumes {
		if v.HostPath != nil {
			b.DataVolumes = append(b.DataVolumes, v.Name)
		}
	}
	c := api.CurvineWorkerExpansion{TypeMeta: metav1.TypeMeta{APIVersion: api.APIVersion, Kind: "CurvineWorkerExpansion"}, ObjectMeta: metav1.ObjectMeta{Name: *name, Namespace: m.Namespace}, Spec: api.WorkerExpansionSpec{Binding: b, Replicas: 1}}
	if err := c.Spec.Validate(); err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(c)
}
