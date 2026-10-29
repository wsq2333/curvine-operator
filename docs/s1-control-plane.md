# S1 Kubernetes 控制面与 Observer 抽象

本文记录 S1 阶段范围。后续 S2 已增加可选资源管理、工作负载 RBAC 和 ResourcesReady/Progressing，参见 [S2 工作负载](s2-workloads.md)；默认观察模式仍保留。

本阶段实现命名空间内的 CR 监听、规格校验、应用状态观察接口和 status 收敛。尚未实现 Curvine 版本适配器、工作负载创建、扩缩容或升级。默认 Observer 返回 `UnsupportedCapability`，集群 Ready 为 Unknown。

## 运行边界

独立二进制 `manager` 负责 Kubernetes 控制面；`curvine-operator` 保留 S0 离线 CLI。manager 不调用 S0 计划执行任何步骤。没有 Kubernetes 连接时，离线 CLI 仍可使用。

| 模块 | 当前实现 |
| --- | --- |
| API | `storage.curvine.io/v1alpha1`、namespaced `CurvineCluster`/List、scheme、DeepCopy、status subresource、打印列 |
| CRD | OpenAPI schema、默认值、枚举、列表唯一性与 CEL 变更约束 |
| manager | 单命名空间 cache、默认启用 leader election、指标、存活/就绪探针、信号退出 |
| controller | 读取规格、Observer 超时、样本校验、Conditions、乐观锁 status patch、冲突后重新观察 |
| Observer | 只读、版本无关的 Target/Snapshot 接口与显式 Unsupported 实现 |
| 权限 | 读取 CR、更新 status、维护 Lease、写事件；不读取 Secret，不管理 Pod/PVC/StatefulSet |
| 发布基础 | Kustomize 清单与 manager Dockerfile；没有实际构建/发布容器或部署到用户集群 |

## 本地开发与检查

```sh
make generate
make check build
ENVTEST_DOWNLOAD=1 make test-envtest
```

依赖固定为 controller-runtime v0.24.1 和 Kubernetes Go 库 v0.36.1；生成工具为 controller-gen v0.21.0。envtest 固定使用 Kubernetes 1.36.0，下载到本地忽略目录 `.local/envtest`；也可设置 `KUBEBUILDER_ASSETS` 使用预先准备的 kube-apiserver/etcd。没有测试二进制时显式失败，不静默跳过。

envtest 明确禁用 existing-cluster 模式，即使环境设置了 `USE_EXISTING_CLUSTER`，也不会访问当前 kubeconfig 指向的集群。它只启动临时本地 API Server 和 etcd，没有 kubelet、scheduler 或 StatefulSet controller。

本轮只验证此测试版本，不声明其他 Kubernetes 版本已兼容。依赖选择参考 [controller-runtime v0.24.1 go.mod](https://github.com/kubernetes-sigs/controller-runtime/blob/v0.24.1/go.mod)；CRD/defaulting/status/CEL 语义参考 [Kubernetes CRD 文档](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definitions/)。

## API 与离线模型

Kubernetes 使用 `CurvineCluster`，包含标准 ObjectMeta 和 status；S0 CLI 继续使用严格的 `Cluster` 意图文档。两者共享 `Spec`，通过 `Intent()` 获取独立副本，避免 metadata/status 混入离线规划。

Kubernetes API 对省略字段补充：`clusterDomain=cluster.local`、`profile=development`、`deletionPolicy=Retain`。S0 CLI 仍要求显式完整输入，不做默认值填充。

CRD 拒绝生产单 Master、非法副本数/容量、重复 Worker 池；通过 CEL 阻止身份、profile、镜像、Master 规格变更，以及 Worker 池删除、缩容和存储变化。新增 Worker 池与扩容意图允许写入，但 S1 不创建或扩容实际工作负载。Worker 池数量上限为 64，以约束列表验证成本。

CRD 覆盖基础结构和变更约束；完整镜像引用语法、namespace 及生成 FQDN 长度等继续由共享 Go 校验检查。通过 admission 不代表 `SpecValid=True`。无效规格不调用 Observer，并明确标记 `SpecValid=False`、`Ready=False`。这是当前无 webhook 方案的边界。

Kubernetes 的未知字段处理遵循 API Server 行为，不能等同于 S0 严格 JSON 解码器；需要拒绝未知字段的 API 调用方应使用 strict field validation。

## Observer 契约

```go
type Observer interface {
    Observe(context.Context, Target) (Snapshot, error)
}
```

Target 提供 CR UID、generation、namespace/name 和独立 Spec 副本。适配器必须只读、遵守 context deadline，且不能把期望副本数当成实际注册数或把 Pod Ready 当作应用健康。

Snapshot 必须带匹配的 UID/generation、采样时间、应用版本，以及 quorum、workers、data availability 三项显式 Health。每项取 Healthy、Unhealthy 或 Unknown；没有证据必须返回 Unknown。RegisteredWorkers 是可选指针，nil 与 0 含义不同。

默认每 30 秒观察一次，每次 5 秒超时；接受的样本最大年龄为 90 秒，允许至多 5 秒的时钟领先。字段、枚举、身份、时间或计数无效时，整个样本被拒绝。context timeout 需要适配器协作，控制器不会为不遵守取消的实现无限派生 goroutine。

默认 `observe.Unsupported` 不访问网络。当前没有通过镜像标签猜测 API 的逻辑，也没有可以启用“模拟健康”的运行参数。测试中的健康样本仅存在于测试代码。

真实适配器接入前，需要固定 Curvine commit/镜像 digest，验证接口路径、返回结构、健康语义、版本报告和错误处理。若某版本无法证明 data availability，应保持该项 Unknown；不能为得到 Ready=True 填造证据。

## 状态与并发

Conditions：`SpecValid`、`ObservationReady`、`QuorumHealthy`、`WorkersHealthy`、`DataAvailable`、`Ready`。每项包含 observedGeneration、reason、message 和 lastTransitionTime。

- 有效应用样本使 ObservationReady=True；只有三项健康要求全部 Healthy，Ready 才为 True。
- 任一明确 Unhealthy 导致 Ready=False；无法确定且没有明确失败时为 Unknown。
- 观察不支持、超时、失败或失效时，清除当前 `status.observation`，将应用健康置为 Unknown，保留 lastSuccessfulObservationTime 供诊断。
- `observedGeneration` 表示已处理该代规格，不表示已经部署或执行成功。
- 观察完成后，通过未缓存的 APIReader 重读资源；UID/generation 已变化时丢弃样本。status patch 带 resourceVersion 乐观锁，冲突后重新观察，不把旧样本重试覆盖到新规格。
- 相同状态不重复 patch；status-only watch 更新不触发新的 reconcile，健康变化通过定时观察发现。保留不属于本控制器的 Condition。
- Adapter 原始错误可能含 URL/凭据，不写入 status，使用固定 reason/message。

控制器停止时不会继续更新健康状态，消费者需同时检查样本时间；旧的 True 不能无限期视为新鲜证据。

## 部署清单的使用方式

以下命令供后续明确选定测试集群后执行，本次没有执行部署：

```sh
docker build -t curvine-operator:dev .
kubectl kustomize config/default
kubectl apply -k config/default
```

默认命名空间 `curvine-system`，manager 从 Pod namespace 确定唯一监听范围。示例镜像 `curvine-operator:dev` 需要先构建并导入测试集群或替换为可拉取的镜像；清单不会自动发布镜像。

Role/RoleBinding、ServiceAccount、Lease 与被观察 CR 必须位于所选命名空间。更改 namespace 时通过 Kustomize 统一调整；CRD 安装是管理员操作，不属于 manager 的运行权限。现有 S0 JSON 示例指定了其他 namespace，实际创建 CR 前必须调整 namespace 和示例镜像/存储值。

本地运行 manager 时，在已安装 CRD 和配置权限的测试环境中执行 `bin/manager --watch-namespace=<namespace>`。leader election 默认开启；metrics 默认只监听 loopback。`/healthz` 表示进程存活，`/readyz` 等待 cache 同步，不代表当前副本是 leader，也不代表 Curvine 集群可用。

## 下一步

本次完成 S1 中不依赖具体 Curvine 版本的部分。下一步是实际 Observer 适配器与应用契约测试；工作负载创建仍属于 S2。安全 drain、成员变更、升级和恢复不在 S1 控制器中实现。
