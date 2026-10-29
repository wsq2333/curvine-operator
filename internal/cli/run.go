package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
	"github.com/wsq2333/curvine-operator/internal/plan"
)

const usage = `curvine-operator: S0 offline design prototype (no Kubernetes writes)
Usage:
  curvine-operator validate --file examples/cluster-development.json
  curvine-operator plan --file desired.json [--previous previous-spec.json]
  curvine-operator version
Exit codes: 0 valid/unblocked advisory plan, 2 invalid input, 3 blocked change.
An unblocked plan is NOT executable or proof of runtime safety.
`

func readCluster(path string) (api.Cluster, error) {
	var c api.Cluster
	f, err := os.Open(path)
	if err != nil {
		return c, err
	}
	defer f.Close()
	c, err = api.Decode(f)
	if err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

func Run(args []string, out, stderr io.Writer) int {
	fail := func(err error) int { fmt.Fprintln(stderr, err); return 2 }
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		if len(args) > 1 {
			return fail(fmt.Errorf("help accepts no arguments"))
		}
		fmt.Fprint(out, usage)
		return 0
	}
	if args[0] == "version" {
		if len(args) != 1 {
			return fail(fmt.Errorf("version accepts no arguments"))
		}
		fmt.Fprintln(out, "curvine-operator 0.0.1-dev (S0-offline)")
		return 0
	}
	if args[0] != "validate" && args[0] != "plan" {
		return fail(fmt.Errorf("unknown command %q", args[0]))
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(out, usage) }
	file := flags.String("file", "", "desired cluster spec JSON")
	var previousFile string
	if args[0] == "plan" {
		flags.StringVar(&previousFile, "previous", "", "previous intent JSON, not live state")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *file == "" || flags.NArg() != 0 {
		return fail(fmt.Errorf("--file is required and positional arguments are not accepted"))
	}
	desired, err := readCluster(*file)
	if err != nil {
		return fail(err)
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if args[0] == "validate" {
		if err := encoder.Encode(map[string]any{"valid": true, "scope": "offline-spec-only"}); err != nil {
			return fail(err)
		}
		return 0
	}
	var previous *api.Cluster
	if previousFile != "" {
		c, err := readCluster(previousFile)
		if err != nil {
			return fail(err)
		}
		previous = &c
	}
	p, err := plan.Build(desired, previous)
	if err != nil {
		return fail(err)
	}
	if err := encoder.Encode(p); err != nil {
		return fail(err)
	}
	if len(p.Blockers) > 0 {
		return 3
	}
	return 0
}
