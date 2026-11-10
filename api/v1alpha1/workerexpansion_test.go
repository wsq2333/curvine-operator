package v1alpha1

import (
	"strings"
	"testing"
)

func TestExpansionBindingValidation(t *testing.T) {
	pin := PinnedObject{Name: "master", UID: "uid", SHA256: strings.Repeat("a", 64)}
	b := WorkerExpansionBinding{Purpose: "ReadCache", ClusterID: "cache", Master: pin, Worker: pin, ConfigMaps: []PinnedObject{pin}, TargetNode: "node-2", TargetNodeUID: "node-uid", Image: "registry.example/curvine@sha256:" + strings.Repeat("b", 64), DataVolumes: []string{"data"}}
	b.Worker.Name = "worker"
	s := WorkerExpansionSpec{Binding: b, Replicas: 2}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, replicas := range []int32{0, 3} {
		s.Replicas = replicas
		if s.Validate() == nil {
			t.Fatal("unsupported replica target accepted")
		}
	}
	s.Replicas = 2
	s.Binding.Purpose = "WriteCache"
	if s.Validate() == nil {
		t.Fatal("write-cache bypass accepted")
	}
	s.Binding = b
	s.Binding.Image = "registry.example/curvine:latest"
	if s.Validate() == nil {
		t.Fatal("unpinned image accepted")
	}
}
