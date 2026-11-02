# Contract fixtures

`workers.json` is a synthetic, reduced response based on the Rust serde fields
in Curvine v0.5.4-alpha, commit `89df141912e96d127421de6e4d36e6e2190d9949`.
It is not a capture from a running Curvine cluster. `${WORKER_HOST}` and
`${NOW_MS}` are substituted by tests. `storage_map` includes only the field
consumed by the adapter; additive upstream fields are intentionally accepted.

The source references, version mismatch between the Cargo package and release
tag, endpoint semantics and unobservable health requirements are recorded in
[the application contract](../../../../docs/contracts/v0.5.4-alpha.md).

Unit tests exercise the HTTP transport/decoder, identities, role transitions,
registration, heartbeat and version checks. The envtest release test uses a
local HTTP server plus a real temporary Kubernetes API and the checked-in
namespace Role. Neither layer proves real Raft or data-plane health.
