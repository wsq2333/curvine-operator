# Working in curvine-operator

- This is an independent sibling project of malta and curvine. Do not modify those repositories as an implicit part of operator work.
- Read README.md and docs/roadmap.md before extending functionality. The S0 CLI is offline. The manager observes by default; `--workload-contract=curvine-728d2a46` opts into the S2 resource engine. `--workload-contract=curvine-v0.5.4-alpha` selects the pinned release resource and HTTP Observer contracts; `--observer-contract=curvine-v0.5.4-alpha` enables observation alone. No flags still selects Unsupported. Read docs/contracts/v0.5.4-alpha.md: the release HTTP API cannot prove quorum or data availability, so ordinary Worker mutations remain blocked. The immutable readCache purpose allows non-destructive Worker allocation after Master startup; it must not claim quorum or relax scale-in/upgrade/storage protections. Read docs/read-cache.md before changing these gates. Read docs/s2-workloads.md before changing resource execution.
- Run `make check build` for Go behavior changes. Use meaningful domain, lifecycle and CLI contract tests.
- Run `make generate` after API marker changes and `make test-envtest` for CRD/controller/RBAC changes. Envtest must use a temporary local control plane, never an existing cluster. See docs/s1-control-plane.md for assets.
- Keep planning pure. A previous spec is not observed runtime state. Never turn an offline plan into Kubernetes writes without current UID, generation, ownership, health and operation-state checks.
- Unknown application health is not healthy. Pod readiness does not prove Raft quorum or data availability.
- Do not implement worker deletion as a replica decrement without verified drain semantics. Do not implement Raft membership changes as StatefulSet scaling.
- Preserve metadata and worker data by default. Never enable format flags, clear host disks, adopt unrelated resources or reattach retained volumes solely by name.
- Clearly distinguish implemented, locally tested and integration-tested capabilities. Update docs with evidence and remaining limits.
- Do not put credentials or real kubeconfigs in source, examples, status or logs.
