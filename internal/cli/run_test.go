package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
)

func TestCLIContract(t *testing.T) {
	for _, name := range []string{"development", "production"} {
		file := "../../examples/cluster-" + name + ".json"
		for _, command := range []string{"validate", "plan"} {
			var out, stderr bytes.Buffer
			if code := Run([]string{command, "--file", file}, &out, &stderr); code != 0 {
				t.Fatalf("code %d: %s", code, &stderr)
			}
			if !json.Valid(out.Bytes()) {
				t.Fatal("output must be JSON")
			}
		}
	}
	for _, args := range [][]string{{"apply"}, {"plan"}, {"plan", "--file", "missing"}, {"version", "extra"}, {"validate", "--file", "../../examples/cluster-development.json", "extra"}} {
		var out, stderr bytes.Buffer
		if code := Run(args, &out, &stderr); code != 2 {
			t.Fatalf("expected exit 2 for %v, got %d", args, code)
		}
	}
}

func TestCLIRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	data, err := os.ReadFile("../../examples/cluster-development.json")
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"unknown":  strings.Replace(string(data), `"spec": {`, `"unexpected": true, "spec": {`, 1),
		"trailing": string(data) + "{}",
	} {
		file := filepath.Join(t.TempDir(), name+".json")
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		if code := Run([]string{"plan", "--file", file}, &out, &stderr); code != 2 {
			t.Fatalf("expected rejection: %s", name)
		}
	}
}

func TestCLIBlockedChangeIsMachineReadable(t *testing.T) {
	oldPath := "../../examples/cluster-production.json"
	data, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	var c api.Cluster
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	c.Spec.WorkerPools[0].Replicas = 1
	data, err = json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "desired.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := Run([]string{"plan", "--file", file, "--previous", oldPath}, &out, &stderr); code != 3 {
		t.Fatalf("expected exit 3, got %d: %s", code, &stderr)
	}
	if !json.Valid(out.Bytes()) || !strings.Contains(out.String(), "WorkerDrainRequired") {
		t.Fatalf("missing structured blocker: %s", &out)
	}
}
