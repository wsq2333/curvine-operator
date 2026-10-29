# S2 工作负载构建与持续收敛

本轮实现 S2 的 Kubernetes 资源执行层，并通过单元测试和本地 envtest。**尚未完成 S2 的真实部署验收**：没有确定可用的 Curvine 镜像 digest、StorageClass 或应用 Observer，未执行真实 Pod 启动、Raft、文件读写和卷恢复测试。

## 启用边界与运行契约

默认 manager 保留只观察模式。显式设置 `--workload-contract=curvine-728d2a46` 才启用工作负载管理；其他非空值会在启动时被拒绝。这个名称表示源码级启动/配置契约，不是已验证的镜像版本，也不意味着任意满足 digest 语法的镜像都兼容。

契约依据 Curvine commit `728d2a46` 的 `ClusterConf`/环境覆盖、容器布局、Master journal 初始化和探针示例。实现要求：

- `/app/curvine/lib/curvine-server --service master|worker --conf /etc/curvine/curvine-cluster.toml`，直接运行服务作为 PID 1。
- `CURVINE_MASTER_HOSTNAME` 同时覆盖 Master 与 journal hostname；Worker 使用 `CURVINE_WORKER_HOSTNAME`。Downward API 和环境变量展开生成完整 Pod FQDN；不使用有 Linux 64 字节限制的 `setHostnameAsFQDN`。
- Master RPC/Raft/Web 端口为 8995/8996/9000，Worker RPC/Web 为 8997/9000。
- 元数据、journal 和 Worker 数据位于 `/var/lib/curvine` PVC 下的独立路径；两种 format 标志都为 false。日志和临时文件使用 emptyDir。
- 容器使用 UID/GID 1000、fsGroup 1000、只读根文件系统，无 privileged/hostNetwork/hostPath、不挂载 API token。镜像可执行文件/动态库和存储驱动必须支持这些约束，尚待真实镜像验证。
- Worker 保留 10% 物理可用空间；无固定 10 GB 预留，避免较小测试卷天然不可写。后续容量策略另行版本化。

manager 仍注入 `observe.Unsupported`。没有真实 Observer 时，Master 和相关资源可以创建，但 Worker StatefulSet 保持 `WaitingForQuorum`。目前**没有仅修改一个运行参数就能得到真实应用观察的适配器**，也没有绕过健康门槛的模拟健康开关。

本地源码 HTTP overview 中的 `master_state`、Worker 数量等字段不能单独证明 quorum、版本与数据可用性；本轮不把这些字段猜测为 Healthy。

## 构建与资源关系

`internal/workload.Build` 是纯函数，输出 typed Kubernetes objects。资源命名使用 CR 名称，选择器绑定 CR UID，Worker 池按名称排序。

| 资源 | 内容与生命周期 |
| --- | --- |
| ConfigMap | 按配置 SHA-256 命名、immutable；Worker 池顺序/扩容不改变配置或 Master 模板 |
| Master StatefulSet | 固定 1/3/5 副本、Parallel 启动、稳定 ordinal 与 Raft ID（ordinal + 1） |
| Worker StatefulSet | 每池一个 StatefulSet；新池和增加副本需要应用健康证据 |
| PVC 模板 | 每 Pod 一个 `data` RWO 卷；StorageClass/容量来自 Spec；带集群名称及 UID 标签，无 ownerReference |
| Headless Service | 每个 StatefulSet 一个；发布未就绪地址，避免 Raft bootstrap 等待 readiness 的循环依赖 |
| PDB | Master 至少保留多数派；Worker 保留所有期望副本，尚未验证数据复制与 drain 前不自动允许自愿驱逐 |

Master 多副本要求按节点反亲和，并使用 zone 拓扑分布约束。节点或 zone 标签不满足时允许 Pod Pending，不静默降级约束。zone 约束不等于已验证的跨可用区容灾承诺。

所有 StatefulSet 使用 `OnDelete` 更新策略。受支持的模板漂移会被修正，但控制器不会删除现存 Pod 推动重启；未达到新 revision 时 `ResourcesReady=False`，等待未来维护流程或管理员明确操作。PDB 不阻止直接删除 Pod、StatefulSet 缩容或节点故障。

## Reconcile 执行顺序

1. 校验 Spec、运行契约及 CR UID/generation。
2. 未缓存读取整个期望资源集合，确认 controller owner 的 API/kind/name/UID。检查卷身份、存储布局、不可变字段和已有多余 StatefulSet；已知冲突会在任何资源写入前阻断。
3. 对全部资源做 API dry-run，检查实际 admission 和权限。对已有资源用 SSA 计算差异；不会把 dry-run 当成实际执行。
4. 按依赖顺序每轮最多实际创建/修改一个资源。先配置、Service/PDB 和 Master，再 Worker。支持资源可先于健康门槛创建，Worker StatefulSet 写入须通过门槛。
5. Worker 创建/变更要求 Master 当前 generation/revision 已就绪且新鲜 Observer 快照报告 quorum 健康；已有 Worker 扩容还要求 Workers/DataAvailability 健康。执行前再次验证快照时效、UID 和 generation。
6. 每次写入前重新读 CR；创建使用 Create，防止并发同名创建被 SSA 接管。已有资源 SSA 使用 UID/resourceVersion 前置条件。冲突后重新观察。
7. 检查全部 StatefulSet 的 observedGeneration、revision、副本数/就绪数/更新数，以及预期 PVC Bound，生成 `ResourcesReady` 和 `Progressing`。整体 Ready 还必须满足应用健康，不能由 Pod Ready 推断。

资源写入后默认 1 秒再次推进；其他等待沿用 30 秒观察周期。API 错误更新固定 reason 后交给队列退避，不在 status 或错误日志中回显原始 API payload。status 写入仍使用乐观锁，保留其他控制器的 Conditions。

监听：CR generation 变化；直接拥有的 StatefulSet、ConfigMap、Service、PDB；通过集群标签路由的 Pod/PVC。子资源 status 更新不受 CR generation filter 影响。标签只用于事件路由，不能授权接管资源。无标签的冲突卷通过定时全量检查发现。

## 字段管理与保护规则

SSA field manager 为 `curvine-operator`。仅在已验证 owner UID 的资源上修复构建器声明的字段，使用 ForceOwnership 恢复被外部修改的受管字段，保留独立维护的标签/注解及未声明字段。额外 Service selector 会阻断，防止服务选择不到受管 Pod 却误报资源就绪。

以下情况阻断，不自动删除、重建或接管：

- 同名资源无 owner 或 owner UID 不同；运行契约标记不同/丢失。
- Master 当前副本数与声明不一致、Worker 当前副本数大于声明、已有池从声明中移除。
- 镜像不同、StatefulSet 存储/服务名/选择器布局不同、配置内容与 hash 不一致。
- 同名保留 PVC 无当前 UID 标签、带 ownerReference、存储类型/大小不符、来自数据源，或正在删除/已经 Lost。

Kubernetes 没有跨 CR 和子资源的事务。全量 preflight、逐次重读和子资源版本前置条件缩小竞态窗口，但不能声称多对象原子提交；外部管理员仍可能并发修改资源。当前路径无缩容、升级、drain、Master 成员变更、Pod 删除或 PVC 写入权限。

## 删除与恢复

删除 CR 后，普通子资源可由 Kubernetes GC 清理；PVC 不随 CR、StatefulSet 删除或缩容而删除（两项 retention policy 均为 Retain）。Operator 不添加依赖 Curvine 网络可达性的删除 Finalizer。

同一 CR incarnation 下，丢失的受管服务/配置/StatefulSet 可以重建，已有卷必须匹配当前 UID 与存储契约。删除后重新创建同名 CR 会得到新 UID，旧资源或旧卷导致冲突，**不会自动导入旧数据**。

保留 PVC 不等于完整备份：人工删除 PVC、StorageClass/PV 回收策略、外部修改 retention policy、实际盘故障和手工导入都需要独立运维与验收。当前没有自动恢复/导入 API；不要通过修改 UID 标签绕过保护。envtest 不运行 GC/CSI，测试显式模拟子资源 GC 和 Bound 状态，不宣称已验证真实卷恢复。

## 验证与后续验收

```sh
make generate
make check build
ENVTEST_DOWNLOAD=1 make test-envtest
kubectl kustomize config/default
kubectl kustomize config/workloads
```

`config/default` 保持观察模式；`config/workloads` 只添加显式运行契约参数。两者仍使用待构建的 Operator 镜像占位值。没有自动部署或推送步骤。

后续在明确镜像与测试集群后才执行真实部署，并记录：

1. 镜像 digest 与上述运行契约一致；Kubernetes/StorageClass/CSI 版本、权限、挂载 UID/GID 验证。
2. 正确实现并验证 Observer 的版本、quorum、Worker 注册与数据可用性语义。
3. 创建 Master → 确认 quorum → 创建 Worker → 文件写入与读回。
4. Operator 重启、Pod 重启、资源漂移后继续读回；OnDelete 待维护状态正确。
5. 删除 CR 后 PVC/PV 留存，新 CR 拒绝自动复用；显式恢复流程的数据比对。

在上述步骤完成前，本阶段是经过本地 API 验证的资源执行实现，不是已通过部署验收的 MVP。
