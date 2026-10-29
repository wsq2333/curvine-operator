# 实施路线与验收门槛

按依赖和验收结果推进，不以未验证的时间估算承诺上线。S0 已完成本地验收。S1 的 Kubernetes 控制面和 Observer 抽象已实现，具体 Curvine 版本适配及应用契约测试待完成；S2 的资源构建、受控执行与监听已通过本地验证，真实部署/读写/恢复验收待完成；S3 及之后尚未完成。参见 [S0 契约](s0.md)、[S1 控制面](s1-control-plane.md) 与 [S2 工作负载](s2-workloads.md)。

| 阶段 | 交付内容 | 验收门槛 | 依赖 |
| --- | --- | --- | --- |
| S0 项目基础 | Go 模型、严格 JSON 校验、可追溯离线计划、保护规则、源码核查和设计 | make check build、真实 CLI 退出码 0/2/3、缩容与混合变更阻断 | 本地 Go |
| S1 应用契约与 K8s 控制面 | 上游适配接口、Kubernetes API types/CRD/status、manager、RBAC、leader election、只读 Observer | envtest 校验默认值/不可变字段/status 冲突；真实应用接口契约测试 | 明确 Curvine 镜像版本、K8s 支持范围 |
| S2 创建与持续收敛 | Master/Worker StatefulSet、PVC、配置与 Service、应用健康、数据保留、漂移修复 | kind/测试集群真实部署与文件读写；重复 reconcile、Operator/Pod 重启、资源同名冲突、CR 删除后数据可恢复 | S1、可用镜像与 StorageClass |
| S3 日常运维 | Worker 扩容、诊断、指标、配置分类与修订、暂停/恢复策略 | 扩容后 Worker 真注册且可承载 I/O；配置失败不破坏健康集群；权限不足显示原因 | S2 |
| S4 受控破坏性操作 | Operation 状态机、Worker drain/缩容、版本升级、逐节点维护 | 在途写不丢；唯一副本/欠副本时拒绝；中途重启继续原操作；升级失败暂停且可诊断 | S3 + 已验证 drain/兼容接口 |
| S5 备份恢复与服务扩展 | Backup/Restore、MDS/FDB 引用、Transfer、Discovery、CSI/Fluid 明确集成 | 新集群恢复后的实际数据比对；依赖失联、租约接管与挂载恢复验证 | S2；各服务独立契约 |
| S6 验证与优化 | Malta 部署后 smoke、升级性能对照、容量与预热建议 | 相同环境和版本证据可追溯；预算和回归阈值明确；建议与执行分离 | S3/S4 + Malta 联调 |

S5 中各服务可以在 S2 后独立开发，但不能绕过对应的数据安全测试。Master 动态成员变更单独立项，待 Raft 外部协议与故障恢复测试成熟后启用；固定副本的生命周期不必等待此功能。

## S0 验收清单

- [x] 独立 Go 模块、CLI、构建命令与 CI 检查入口。
- [x] 规格模型、开发/生产示例、扩容/缩容示例。
- [x] JSON 字段、类型、重复键、null、UTF-8、大小上限与镜像引用校验。
- [x] 确定性的创建/差异计划、集群身份、新旧规格哈希与副本数变化。
- [x] Master 成员变更、Worker 缩容/移除、升级、存储和身份迁移保护。
- [x] 纯函数规划不修改输入；混合变更整体阻断；计划始终不可执行。
- [x] 单元测试、真实二进制验收、race、vet、格式检查与构建通过。
- [x] 设计、源码依据、CLI 契约与本地验证记录齐备。

远端 CI 与集群测试不在已完成声明内。上述勾选只对应 [本地验证记录](validation.md)。

## S1 与后续工作包

1. **观察接口：抽象已完成，适配待完成。** 已有只读 `Observer.Observe`、Target/Snapshot、Unknown/Unsupported 和样本有效性契约。下一步固定真实 Curvine 版本并实现适配与契约测试；drain 等写操作不加入当前只读接口。
2. **API 与控制面：已实现。** Kubernetes metadata/status、DeepCopy、schema、打印列、CEL、namespaced manager、RBAC、leader election 与 status reconcile 已落地。group 仍为早期 `storage.curvine.io/v1alpha1` 契约，尚未做跨版本 API 迁移。
3. **纯资源构建器：已实现并本地验证。** 用 typed Kubernetes objects 生成 Master/Worker、PVC、Services、ConfigMap 与 PDB；核验 Pod FQDN、Raft IDs、存储挂载和探针。对生成结果做结构性测试，不只做快照文本比较。
4. **工作负载 reconcile：已实现并本地验证。** 显式运行契约启用资源管理；全量冲突检查、dry-run、每轮至多一次写入、SSA 漂移修复、UID/RV 保护、子资源监听和 PVC Retain 已落地。Worker 创建/扩容受应用健康门控；默认 Observer 未适配时停在等待 quorum。
5. **用一套实际镜像打通 E2E。** 验证创建 → 写文件 → 重启 Pod/控制器 → 读回 → 删除 CR → 保留卷。记录镜像 digest、Kubernetes 版本和 StorageClass，完成后再称为可部署 MVP。

## 验证分层

| 层次 | 能证明什么 | 不能证明什么 |
| --- | --- | --- |
| 单元测试 | 规格约束、差异规划、阻断、状态迁移 | Kubernetes admission、存储和 Curvine 语义 |
| envtest | CRD schema/CEL、watch/reconcile、status、冲突与所有权 | 真实 kubelet、调度、PV、Raft、CSI |
| kind/集成集群 | 创建和重建、配置、DNS、实际应用读写、控制器恢复 | 特定生产盘/网络设备性能与所有故障域 |
| 故障注入 | leader 丢失、分区、卷不可用、drain 中断、重复请求、恢复完整性 | 未覆盖硬件/版本组合的普遍保证 |
| Malta 基准 | 同条件吞吐/延迟与资源效率对照 | 数据一致性、备份完整性与无丢失承诺 |

每个被称为“支持”的操作都需要：前置条件、幂等键、状态观测、超时策略、失败恢复、取消边界、权限要求和至少一个中断恢复用例。

## 发行门槛

- 公布 Operator/Curvine/Kubernetes/CSI 版本矩阵与数据布局兼容范围。
- 提供安装、升级、卸载及保留数据恢复文档，CRD 升级有迁移测试。
- 镜像、依赖、构建产物可追溯；明确许可证和发布仓库。
- 指标与诊断可定位 Pending/Blocked/Degraded；敏感值不进入状态和诊断包。
- 自动升级、自动缩容、自动调优在对应故障恢复演练通过前保持关闭。
