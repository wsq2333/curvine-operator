package v1alpha1

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestReadCacheSpecAndStrictDecoder(t *testing.T) {
	data, err := os.ReadFile("../../examples/cluster-read-cache.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Decode(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*ReadCacheSpec){
		"credentials-in-url": func(r *ReadCacheSpec) { r.Endpoint = "https://user:password@s3.example.com" },
		"traversal":          func(r *ReadCacheSpec) { r.Probe.Object = "../secret" },
		"encoded-traversal":  func(r *ReadCacheSpec) { r.Probe.Object = "%2e%2e/secret" },
		"mutable":            func(r *ReadCacheSpec) { r.Consistency = "Mutable" },
		"wrong-backend":      func(r *ReadCacheSpec) { r.Backend = "OSS" },
		"query":              func(r *ReadCacheSpec) { r.Source += "?token=secret" },
		"oversize":           func(r *ReadCacheSpec) { r.Probe.SizeBytes = 1 << 30 },
		"concurrency":        func(r *ReadCacheSpec) { r.ReadConcurrency = 0 },
		"budget":             func(r *ReadCacheSpec) { r.MinFreePercent = 0 },
		"empty-secret":       func(r *ReadCacheSpec) { r.CredentialsSecret = "" },
	} {
		t.Run(name, func(t *testing.T) {
			r := *c.Spec.ReadCache
			edit(&r)
			if r.Validate() == nil {
				t.Fatal("invalid cache spec accepted")
			}
		})
	}
	oss := *c.Spec.ReadCache
	oss.Backend = "OSS"
	oss.Source = "oss://example-datasets/immutable-v1"
	if err := oss.Validate(); err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
	spec := raw["spec"].(map[string]any)
	spec["readCache"].(map[string]any)["secretKey"] = "never-accepted"
	bad, _ := json.Marshal(raw)
	if _, err := Decode(strings.NewReader(string(bad))); err == nil {
		t.Fatal("inline credentials accepted")
	}
}
