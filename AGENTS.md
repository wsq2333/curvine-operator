# Working in curvine-operator

- This is an independent sibling project of malta and curvine. Do not modify those repositories as an implicit part of operator work.
- Read README.md and docs/roadmap.md before extending functionality. S0 is an offline planning prototype, not a deployable operator.
- Run `make check build` for Go behavior changes. Use meaningful domain, lifecycle and CLI contract tests.
- Keep planning pure. A previous spec is not observed runtime state. Never turn an offline plan into Kubernetes writes without current UID, generation, ownership, health and operation-state checks.
- Unknown application health is not healthy. Pod readiness does not prove Raft quorum or data availability.
- Do not implement worker deletion as a replica decrement without verified drain semantics. Do not implement Raft membership changes as StatefulSet scaling.
- Preserve metadata and worker data by default. Never enable format flags, clear host disks, adopt unrelated resources or reattach retained volumes solely by name.
- Clearly distinguish implemented, locally tested and integration-tested capabilities. Update docs with evidence and remaining limits.
- Do not put credentials or real kubeconfigs in source, examples, status or logs.
