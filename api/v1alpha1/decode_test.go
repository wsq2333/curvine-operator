package v1alpha1

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDecodeRejectsAmbiguousOrMalformedInput(t *testing.T) {
	data, err := os.ReadFile("../../examples/cluster-production.json")
	if err != nil {
		t.Fatal(err)
	}
	valid := string(data)
	cases := map[string]struct{ input, reason string }{
		"empty":             {"", "EOF"},
		"null-root":         {"null", "null"},
		"array-root":        {"[]", "expected object"},
		"missing-fields":    {"{}", "expected storage.curvine.io"},
		"unknown":           {strings.Replace(valid, `"spec":`, `"extra": 1, "spec":`, 1), "unknown field"},
		"wrong-case":        {strings.Replace(valid, `"replicas":`, `"Replicas":`, 1), "case-sensitive"},
		"duplicate-root":    {strings.Replace(valid, `"spec":`, `"kind": "CurvineCluster", "spec":`, 1), "duplicate field"},
		"duplicate-nested":  {strings.Replace(valid, `"replicas": 3`, `"replicas": 1, "replicas": 3`, 1), "duplicate field"},
		"escaped-duplicate": {strings.Replace(valid, `"replicas": 3`, `"replicas": 1, "repl\u0069cas": 3`, 1), "duplicate field"},
		"null-field":        {strings.Replace(valid, `"replicas": 3`, `"replicas": null`, 1), "null"},
		"null-pool":         {strings.Replace(valid, `"workerPools": [`, `"workerPools": [null,`, 1), "null"},
		"wrong-type":        {strings.Replace(valid, `"replicas": 3`, `"replicas": "3"`, 1), "expected integer"},
		"fraction":          {strings.Replace(valid, `"replicas": 3`, `"replicas": 3.5`, 1), "cannot unmarshal"},
		"overflow":          {strings.Replace(valid, `"replicas": 3`, `"replicas": 999999999999999999999999`, 1), "cannot unmarshal"},
		"trailing-document": {valid + "{}", "exactly one"},
		"trailing-garbage":  {valid + "x", "exactly one"},
		"truncated":         {valid[:len(valid)/2], ""},
		"invalid-utf8":      {valid + string([]byte{0xff}), "UTF-8"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(tc.input))
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("expected %q rejection, got %v", tc.reason, err)
			}
		})
	}
}

func TestDecodeSizeBoundaryAndRoundTrip(t *testing.T) {
	c := validCluster(t)
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	padded := append(data, bytes.Repeat([]byte(" "), MaxSpecBytes-len(data))...)
	got, err := Decode(bytes.NewReader(padded))
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := json.Marshal(got)
	if err != nil || !bytes.Equal(data, reencoded) {
		t.Fatalf("round trip changed spec: %v", err)
	}
	if _, err := Decode(bytes.NewReader(append(padded, ' '))); err == nil {
		t.Fatal("accepted oversized spec")
	}
}

func TestImageReferenceContract(t *testing.T) {
	hash := "@sha256:" + strings.Repeat("a", 64)
	for _, repo := range []string{"curvine", "team/curvine", "registry.example.com/team/curvine", "localhost:5000/curvine", "registry.example.com:65535/team/curvine_test"} {
		c := validCluster(t)
		c.Spec.Image = repo + hash
		if err := c.Validate(); err != nil {
			t.Errorf("valid %s: %v", repo, err)
		}
	}
	for _, repo := range []string{"curvine/", "curvine//worker", "registry..example/curvine", "registry.example:0/curvine", "registry.example:65536/curvine", "registry.example:+80/curvine", "registry.example:/curvine", "curvine:latest", "team/curvine:tag", "https://registry.example/curvine", "TEAM/curvine", "team/_curvine"} {
		c := validCluster(t)
		c.Spec.Image = repo + hash
		if err := c.Validate(); err == nil {
			t.Errorf("accepted invalid/unsupported repository %s", repo)
		}
	}
}
