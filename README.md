# Curvine Operator

**English** | [简体中文](README_zh.md)

Curvine Operator aims to manage the lifecycle of Curvine clusters on Kubernetes, with a focus on data safety, reliable operations, and observability.

## Status

The project is an early prototype. Offline planning, a Kubernetes control plane, and opt-in workload reconciliation are implemented. A read-only HTTP Observer for Curvine v0.5.4-alpha is implemented; quorum/data-availability proof and Kubernetes end-to-end deployment validation remain pending. An opt-in S3/OSS read-cache contract with read-only FUSE is implemented; see the scoped validation evidence below.

## Current capabilities

- Define and validate cluster specifications.
- Generate offline plans for cluster creation and worker expansion.
- Flag changes that require dedicated workflows, including scale-in, upgrades, and storage migration.
- Observe Master roles, Worker registration, heartbeat freshness and reported build provenance through a release-specific read-only Observer.
- Generate and reconcile StatefulSets, retained volume templates, Services, configuration, and disruption budgets.
- Register an immutable S3/OSS source through a Secret-backed mount Job, manage opted-in FUSE nodes, and report scoped read/cache/origin health.
- Watch managed resources, repair supported drift, and refuse conflicting ownership or unsafe replica changes.

Offline plans remain advisory. Workload management requires an explicit runtime contract. The v0.5.4-alpha HTTP contract keeps unprovable quorum and data availability Unknown. Ordinary deployments block Worker mutations without quorum evidence; read-cache deployments allow Worker allocation after Master startup while keeping destructive operations blocked.

## Roadmap

- **Cluster lifecycle:** deployment, persistent storage, health monitoring, and reconciliation.
- **Safe operations:** worker draining, controlled upgrades, and backup and recovery.
- **Integration:** metadata and transfer services, service discovery, and CSI/FUSE.
- **Optimization:** capacity planning, data preloading, and performance validation.

## Documentation

- [S3/OSS read-cache and FUSE contract](docs/read-cache.md) (Chinese)
- [Read-cache validation and remaining limits](docs/validation-read-cache.md) (Chinese)
- [Offline validation and planning](docs/s0.md) (Chinese)
- [Control plane and Observer](docs/s1-control-plane.md) (Chinese)
- [Workload reconciliation](docs/s2-workloads.md) (Chinese)
- [v0.5.4-alpha application contract](docs/contracts/v0.5.4-alpha.md) (Chinese)
- [Design](docs/design.md) (Chinese)
- [Roadmap](docs/roadmap.md) (Chinese)
