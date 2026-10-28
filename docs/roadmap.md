# 实施路线与验收门槛

按依赖和验收结果推进，不以未验证的时间估算承诺上线。S0 已实现；S1 及之后均未实现。

| 阶段 | 交付内容 | 验收门槛 | 依赖 |
| --- | --- | --- | --- |
| S0 项目基础 | Go 模型、离线校验/计划、保护规则、源码核查和设计 | make check build、CLI 退出码与阻断案例 | 本地 Go |
| S1 应用契约与 K8s 控制面 | 上游适配接口、Kubernetes API types/CRD/status、manager、RBAC、leader election、只读 Observer | envtest 校验默认值/不可变字段/status 冲突；真实应用接口契约测试 | 明确 Curvine 镜像版本、K8s 支持范围 |
| S2 创建与持续收敛 | Master/Worker StatefulSet、PVC、配置与 Service、应用健康、数据保留、漂移修复 | kind/测试集群真实部署与文件读写；重复 reconcile、Operator/Pod 重启、资源同名冲突、CR 删除后数据可恢复 | S1、可用镜像与 StorageClass |
| S3 日常运维 | Worker 扩容、诊断、指标、配置分类与修订、暂停/恢复策略 | 扩容后 Worker 真注册且可承载 I/O；配置失败不破坏健康集群；权限不足显示原因 | S2 |
| S4 受控破坏性操作 | Operation 状态机、Worker drain/缩容、版本升级、逐节点维护 | 在途写不丢；唯一副本/欠副本时拒绝；中途重启继续原操作；升级失败暂停且可诊断 | S3 + 已验证 drain/兼容接口 |
| S5 备份恢复与服务扩展 | Backup/Restore、MDS/FDB 引用、Transfer、Discovery、CSI/Fluid 明确集成 | 新集群恢复后的实际数据比对；依赖失联、租约接管与挂载恢复验证 | S2；各服务独立契约 |
| S6 验证与优化 | Malta 部署后 smoke、升级性能对照、容量与预热建议 | 相同环境和版本证据可追溯；预算和回归阈值明确；建议与执行分离 | S3/S4 + Malta 联调 |

S5 中各服务可以在 S2 后独立开发，但不能绕过对应的数据安全测试。Master 动态成员变更单独立项，待 Raft 外部协议与故障恢复测试成熟后启用；固定副本的生命周期不必等待此功能。

## 最近一轮可直接实施的工作包

1. **定义 CurvineAdmin 接口。** `ObserveCluster`、`ObserveWorker`、`BeginDrain`、`ObserveDrain` 等是拟议接口名；实现前映射到真实协议，明确时效、幂等、身份与错误分类。只读观察先落地。
2. **把 API 草案升级成真实 CRD。** 增加 Kubernetes metadata/status、DeepCopy、schema、打印列与文档；核心静态限制进入 CEL/admission。确定 group/domain 的归属与发布路径，不能沿用草案就视为正式承诺。
3. **实现纯资源构建器。** 用 typed Kubernetes objects 生成 Master/Worker、PVC、Services、ConfigMap 与 PDB；核验 Pod FQDN、Raft IDs、存储挂载和探针。对生成结果做结构性测试，不只做快照文本比较。
4. **实现最小 reconcile。** 读取实际对象、拒绝错误所有权、有限步推进、status 条件、冲突重试与 Events；先保持固定 Master 规模，拒绝高风险字段变更。
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
