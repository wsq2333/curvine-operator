# Curvine Operator

[English](README.md) | **简体中文**

Curvine Operator 致力于管理 Kubernetes 上 Curvine 集群的完整生命周期，重点关注数据安全、可靠运维和可观测性。

## 当前状态

项目处于早期原型阶段，已实现离线规划、Kubernetes 控制面及可显式启用的工作负载收敛。真实 Curvine Observer 适配器和端到端部署验收仍待完成。

## 已有能力

- 定义并校验集群规格。
- 为集群创建和 Worker 扩容生成离线计划。
- 识别需要专门流程的变更，包括缩容、升级和存储迁移。
- 通过只读 Observer 接口观察应用状态。
- 生成并收敛 StatefulSet、保留卷模板、Service、配置及中断预算。
- 监听受管资源、修复受支持的漂移，阻止所有权冲突与危险副本变更。

离线计划仅供参考。工作负载管理需显式选择运行契约；Worker 等待 quorum 证据，配置兼容的 Observer 前应用健康保持未知。

## 演进方向

- **集群生命周期：** 部署、持久化存储、健康观测与状态收敛。
- **安全运维：** Worker 数据排空、受控升级与备份恢复。
- **服务集成：** 元数据与传输服务、服务发现和 CSI/FUSE。
- **运行优化：** 容量规划、数据预热与性能验证。

## 文档

- [离线校验与变更规划](docs/s0.md)
- [控制面与 Observer](docs/s1-control-plane.md)
- [工作负载收敛](docs/s2-workloads.md)
- [能力设计](docs/design.md)
- [实施路线](docs/roadmap.md)
