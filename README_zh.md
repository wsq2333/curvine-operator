# Curvine Operator

[English](README.md) | **简体中文**

Curvine Operator 致力于管理 Kubernetes 上 Curvine 集群的完整生命周期，重点关注数据安全、可靠运维和可观测性。

## 核心架构

Operator 使用统一的控制循环管理 Curvine 集群，通过运行契约和能力检查适配不同版本、存储布局及部署用途，包括读缓存和写缓存集群。这是项目的目标范围；当前可执行的操作仍受下文所述契约约束。

```mermaid
flowchart TB
  CR["CurvineCluster：期望状态与运行状态"] -->|监听期望状态| Controller
  subgraph Operator["Curvine Operator"]
    Controller["Controller：持续收敛与安全检查"] --> Engine["工作负载引擎"]
    Observer["应用 Observer"] -->|观测健康状态| Controller
  end
  Controller -->|更新状态| CR
  Engine -->|收敛资源| Resources["Kubernetes 资源：工作负载、存储与配置"]
  Resources -->|资源事件| Controller
  Resources -->|运行| Cluster["Curvine 集群：Master 与 Worker"]
  Cluster -->|应用状态| Observer
```

Kubernetes 负责运行和调度工作负载。Operator 对比期望状态与 Kubernetes、应用层观测，执行受支持的变更，并报告进度或阻塞原因。

## 当前状态

项目处于早期原型阶段，已实现离线规划、Kubernetes 控制面及可显式启用的工作负载收敛。已实现 Curvine v0.5.4-alpha 的只读 HTTP Observer；quorum/数据可用性证明和Kubernetes 端到端部署验收仍待完成。新增可显式启用的 S3/OSS + 只读 FUSE 缓存契约，验证范围见下方记录。

## 已有能力

- 定义并校验集群规格。
- 为集群创建和 Worker 扩容生成离线计划。
- 识别需要专门流程的变更，包括缩容、升级和存储迁移。
- 通过发布版只读 Observer 观察 Master 角色、Worker 注册、心跳新鲜度和上报的构建来源。
- 生成并收敛 StatefulSet、保留卷模板、Service、配置及中断预算。
- 通过引用 Secret 的 Mount Job 注册不可变 S3/OSS 来源，管理显式选择节点上的 FUSE，报告有探针范围的读路径、命中和来源状态。
- 监听受管资源、修复受支持的漂移，阻止所有权冲突与危险副本变更。

离线计划仅供参考。工作负载管理需显式选择运行契约；v0.5.4-alpha HTTP 契约无法证明 quorum 和数据可用性，因此这两项保持 Unknown。普通模式继续阻止缺少 quorum 证据的 Worker 变更；读缓存模式允许 Master 启动后分配 Worker，破坏性操作仍被阻断。

## 演进方向

- **集群生命周期：** 部署、持久化存储、健康观测与状态收敛。
- **安全运维：** Worker 数据排空、受控升级与备份恢复。
- **服务集成：** 元数据与传输服务、服务发现和 CSI/FUSE。
- **运行优化：** 容量规划、数据预热与性能验证。

### 下一里程碑：现有集群接入与 Worker 扩容

**规划中；尚不支持直接扩容现有 Helm 部署。** 将统一控制循环扩展到现有 Curvine 部署，保留集群身份与数据。首个验收场景是在不同的已准备存储节点上将读缓存 Worker 从一个扩到两个，保持现有 Master 不变。该场景是面向更广泛集群支持的阶段性里程碑，不限定 Operator 的目标范围。新增 Kubernetes 节点本身不会改变 Worker 的期望副本数。

待完成工作：

- [ ] 明确 Helm 交接或仅管理 Worker 的接入模式，为受管字段指定唯一管理者，并引用现有 Master。
- [ ] 增加 Worker 节点池选择、强制反亲和、容忍及资源请求，确保两个 Worker 分布在不同的合格节点。
- [ ] 支持本地多块 NVMe、挂载与设备校验、磁盘就绪诊断；保留已有数据，不自动格式化磁盘。
- [ ] 适配主机网络、端口、配置路径和 Worker 注册身份，验证运行版本来源与应用兼容性。
- [ ] 验收 1 → 2 扩容期间的持续 FUSE 读取、新 Worker 注册及实际 I/O，以及 Operator 重启后的持续收敛。

节点供应和磁盘准备属于基础设施前提。本目标不包含自动决定副本数、缓存重平衡、缩容或 Master 成员变更；写缓存安全门槛保持不变。

## 文档

- [S3/OSS 读缓存与 FUSE 契约](docs/read-cache.md)
- [读缓存验证与当前限制](docs/validation-read-cache.md)
- [离线校验与变更规划](docs/s0.md)
- [控制面与 Observer](docs/s1-control-plane.md)
- [工作负载收敛](docs/s2-workloads.md)
- [v0.5.4-alpha 应用契约](docs/contracts/v0.5.4-alpha.md)
- [能力设计](docs/design.md)
- [实施路线](docs/roadmap.md)
