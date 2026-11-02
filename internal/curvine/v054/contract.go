// Package v054 implements the read-only HTTP contract audited at the immutable
// v0.5.4-alpha release commit. A release selection is not runtime version proof.
package v054

import (
	"fmt"
	"strings"

	api "github.com/wsq2333/curvine-operator/api/v1alpha1"
)

const (
	Contract      = "curvine-v0.5.4-alpha"
	Release       = "v0.5.4-alpha"
	SourceCommit  = "89df141912e96d127421de6e4d36e6e2190d9949"
	MasterRPCPort = 8995
	RaftPort      = 8996
	WorkerRPCPort = 8997
	WebPort       = 9000 // Explicitly configured for both roles by this operator.
	OverviewPath  = "/api/overview"
	WorkersPath   = "/api/workers"
)

func MasterHost(name, namespace, domain string, ordinal int) string {
	return fmt.Sprintf("%s-master-%d.%s-master.%s.svc.%s", name, ordinal, name, namespace, domain)
}
func WorkerHost(name, namespace, domain, pool string, ordinal int) string {
	return fmt.Sprintf("%s-worker-%s-%d.%s-worker-%s.%s.svc.%s", name, pool, ordinal, name, pool, namespace, domain)
}

// Cargo's package release_version is 0.2.2 in this tag unless BUILD_VERSION
// overrides it. Match build provenance, never equate that field with the tag.
func matchingBuild(commit, tag string) bool {
	return len(commit) >= 7 && len(commit) <= len(SourceCommit) && strings.HasPrefix(SourceCommit, commit) && (tag == "" || tag == "unknown" || tag == Release)
}

func capabilities() []api.CapabilityStatus {
	return []api.CapabilityStatus{
		{Name: "MasterRoles", Supported: true, Reason: "OverviewEndpoint"},
		{Name: "WorkerInventory", Supported: true, Reason: "WorkersEndpoint"},
		{Name: "WorkerVersion", Supported: true, Reason: "ReportedComponentInfo"},
		{Name: "MasterVersion", Supported: false, Reason: "NotExposedByHTTP"},
		{Name: "Quorum", Supported: false, Reason: "NoQuorumProofEndpoint"},
		{Name: "DataAvailability", Supported: false, Reason: "NoDataReadProofEndpoint"},
	}
}
