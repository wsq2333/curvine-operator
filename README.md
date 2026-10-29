# Curvine Operator

**English** | [简体中文](README_zh.md)

Curvine Operator aims to manage the lifecycle of Curvine clusters on Kubernetes, with a focus on data safety, reliable operations, and observability.

## Status

The project is an early prototype. Offline planning, a Kubernetes control plane, and opt-in workload reconciliation are implemented. A real Curvine Observer adapter and end-to-end deployment validation remain pending.

## Current capabilities

- Define and validate cluster specifications.
- Generate offline plans for cluster creation and worker expansion.
- Flag changes that require dedicated workflows, including scale-in, upgrades, and storage migration.
- Observe application status through a read-only Observer interface.
- Generate and reconcile StatefulSets, retained volume templates, Services, configuration, and disruption budgets.
- Watch managed resources, repair supported drift, and refuse conflicting ownership or unsafe replica changes.

Offline plans remain advisory. Workload management requires an explicit runtime contract. Workers wait for quorum evidence; application health remains unknown until a compatible Observer is configured.

## Roadmap

- **Cluster lifecycle:** deployment, persistent storage, health monitoring, and reconciliation.
- **Safe operations:** worker draining, controlled upgrades, and backup and recovery.
- **Integration:** metadata and transfer services, service discovery, and CSI/FUSE.
- **Optimization:** capacity planning, data preloading, and performance validation.

## Documentation

- [Offline validation and planning](docs/s0.md) (Chinese)
- [Control plane and Observer](docs/s1-control-plane.md) (Chinese)
- [Workload reconciliation](docs/s2-workloads.md) (Chinese)
- [Design](docs/design.md) (Chinese)
- [Roadmap](docs/roadmap.md) (Chinese)
