// Package acceptance exercises the real CLI process, including os.Exit codes.
package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBinaryContract(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "curvine-operator")
	build := exec.Command("go", "build", "-o", binary, "./cmd/curvine-operator")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	file := func(name string) string { return filepath.Join(root, "examples", name) }
	previous := file("cluster-production.json")
	invalid := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalid, []byte(`{"kind":"CurvineCluster","kind":"CurvineCluster"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		args       []string
		code       int
		jsonOutput bool
		contains   string
	}{
		{"validate", []string{"validate", "--file", file("cluster-development.json")}, 0, true, `"valid": true`},
		{"bootstrap", []string{"plan", "--file", previous}, 0, true, `"mode": "bootstrap"`},
		{"noop", []string{"plan", "--file", previous, "--previous", previous}, 0, true, `"steps": []`},
		{"scale-out", []string{"plan", "--file", file("cluster-scale-out.json"), "--previous", previous}, 0, true, `"to": 4`},
		{"scale-in", []string{"plan", "--file", file("cluster-scale-in.json"), "--previous", previous}, 3, true, "WorkerDrainRequired"},
		{"invalid", []string{"validate", "--file", invalid}, 2, false, "duplicate field"},
		{"invalid-previous", []string{"plan", "--file", previous, "--previous", invalid}, 2, false, "duplicate field"},
		{"missing-file", []string{"plan", "--file", filepath.Join(t.TempDir(), "missing")}, 2, false, "no such file"},
		{"unknown-command", []string{"apply"}, 2, false, "unknown command"},
		{"help", []string{"plan", "--help"}, 0, false, "Usage:"},
		{"version", []string{"version"}, 0, false, "S0-offline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(binary, tc.args...)
			cmd.Dir = t.TempDir() // Inputs must not depend on a repository cwd.
			cmd.Env = append(os.Environ(), "KUBECONFIG="+filepath.Join(cmd.Dir, "absent-kubeconfig"))
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != tc.code {
				t.Fatalf("exit %d, want %d; stderr=%s", code, tc.code, &stderr)
			}
			if tc.jsonOutput {
				if !json.Valid(stdout.Bytes()) || stderr.Len() != 0 {
					t.Fatalf("invalid streams: stdout=%s stderr=%s", &stdout, &stderr)
				}
				if tc.args[0] == "plan" {
					var p struct {
						Executable *bool `json:"executable"`
						Steps      []any `json:"steps"`
					}
					if err := json.Unmarshal(stdout.Bytes(), &p); err != nil {
						t.Fatal(err)
					}
					if p.Executable == nil || *p.Executable {
						t.Fatal("plan must explicitly be non-executable")
					}
					if code == 3 && len(p.Steps) != 0 {
						t.Fatal("blocked plan has steps")
					}
				}
			}
			output := stdout.String()
			if code == 2 {
				if stdout.Len() != 0 {
					t.Fatalf("invalid input produced stdout: %s", &stdout)
				}
				output = stderr.String()
			}
			if !strings.Contains(output, tc.contains) {
				t.Fatalf("missing %q in %s", tc.contains, output)
			}
		})
	}
}
