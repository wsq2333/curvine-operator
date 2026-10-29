# S2 资源执行层本地验证

日期：2026-10-29。S0/S1 原有未提交成果与本轮 S2 实现一起验证。本页只记录本地证据；不声明远端 CI、容器镜像发布或真实集群部署通过。

## 通过的检查

- `make generate`：固定 controller-gen v0.21.0，生成 CRD 与 DeepCopy。
- `make check build`：全量 Go race 测试、API/CLI/计划/Observer/Controller/资源构建测试、真实 CLI 验收、vet、gofmt 与两个二进制构建。
- `make test-envtest`：使用 Kubernetes 1.36.0 的临时本地 API Server/etcd，显式禁用 existing-cluster，不使用用户 kubeconfig。
- `kubectl kustomize config/default` 与 `config/workloads`：观察模式和显式资源管理模式均可渲染。
- `git diff --check`：提交前检查空白错误。

## 新增覆盖

| 场景 | 验证结果 |
| --- | --- |
| 纯构建器 | 输入不变、确定性排序，Worker 扩容/加池不改变 Master 和配置；DNS、Raft ID、持久路径与探针结构检查 |
| 存储/权限默认值 | format=false、Retain/Retain、PVC 模板无 owner、UID 标签、无 hostPath/privileged、非 root、只读根目录 |
| 实际资源 admission | 在受限 ServiceAccount 权限下创建 ConfigMap、Services、PDB、Master/Worker StatefulSet |
| 健康门控 | Master 就绪不等于 quorum；无/过期/错 UID 快照拒绝 Worker 创建；应用健康未知拒绝已有 Worker 扩容 |
| 幂等性与漂移 | 重复 reconcile 不改变资源 resourceVersion；修复受管 Service 字段并保留用户标签 |
| 恢复与安全变更 | 新 Engine 实例从已有资源继续；Worker 扩容不改变 Master generation；不把较大当前副本数减回期望值 |
| 所有权冲突 | 任一后续资源冲突阻止前面资源创建；无 owner 同名资源不接管；无标签/旧 UID 的保留 PVC 阻断 bootstrap |
| 并发竞态 | preflight 后 CR generation 变化拒绝写入；并发同名 Create 返回冲突，不退化成 SSA 接管 |
| 删除与保留 | CR 重建不接管旧资源；模拟普通子资源 GC 后仍拒绝旧 PVC，保留 PVC 无 owner 且保留原 UID |
| Watch | 一小时 poll 间隔下 Service 变更自动修复；无 owner 的 PVC 事件通过标签触发资源冲突状态 |
| RBAC | 禁止 CR 创建/规格修改、Secret 读取、跨 namespace 访问、Pod/PVC/StatefulSet 删除和 PVC 创建 |
| S1 回归 | 默认观察模式、Unsupported/Unknown、status 幂等、Observer 状态迁移、generation、CRD/CEL、选主 Lease |

## 证据边界

envtest 没有 StatefulSet controller、kubelet、scheduler、GC 或 CSI。测试由管理员身份显式写入 StatefulSet Ready/revision 和 PVC Bound 状态，并模拟子资源 GC；测试样本 Observer 仅存在于测试代码。不能据此认定真实镜像可运行、卷可恢复、Master quorum 健康或文件 I/O 正确。

真实 Curvine Observer、镜像/StorageClass 兼容、文件写读、Pod 故障恢复及真实 GC/Retain 验收仍待完成。下一步验收清单见 [S2 文档](s2-workloads.md)。
