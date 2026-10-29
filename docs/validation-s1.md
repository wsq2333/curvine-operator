# S1 控制面与 Observer 抽象验证

本文保留 S1 阶段的验证范围。S2 新增的工作负载权限与测试见 [S2 验证记录](validation-s2.md)。

日期：2026-10-29。环境：macOS arm64，Go 1.26.5；envtest 使用临时 Kubernetes 1.36.0 API Server/etcd。没有使用现有 kubeconfig 集群，没有部署 Curvine 工作负载，也没有提交或推送本轮改动。

## 已通过的检查

| 检查 | 结果与范围 |
| --- | --- |
| `make generate` | 固定 controller-gen 生成 CRD 与 DeepCopy |
| 生成物复核 | 重复生成的 CRD/DeepCopy 哈希一致；Kustomize 默认清单渲染成功，namespace 与 RoleBinding 一致 |
| `make check build` | 单元测试、race、实际离线 CLI 验收、vet、格式检查、CLI/manager 构建通过 |
| `make test-envtest` | 真实 API Server 下的 CRD、CEL、RBAC、leader election、watch 和 status 测试通过 |

## 应用观察契约与控制器测试

- 默认 Unsupported 返回 Unknown，不伪造健康；相同状态不重复 patch。
- 样本必须绑定 UID/generation，携带有效采样时间、版本与明确的健康枚举。
- 缺失、过期、未来时间、错误 UID/generation、非法计数和隐式健康被拒绝。
- timeout、观察失败、健康转 Unknown/Unhealthy 时，清除当前样本并更新 Conditions；保留最后成功观察时间。
- 无效规格不调用 Observer；适配器原始错误不进入 status。
- 观察期间 generation 变化时拒绝旧样本；status 冲突后重新观察。
- 保留其他控制器的 Condition，Target/Intent/DeepCopy 不共享可变规格切片。

## 真实 API Server 验证

- 默认值生效；生产单 Master、偶数 Master、重复池、错误存储名/域名和 Delete 策略被拒绝。
- 身份、镜像、Master 规格、Worker 缩容/替换与存储修改被 CEL 拒绝；Worker 扩容意图可更新并递增 generation。
- 创建和普通 update 无法写入 status；status 更新不递增 generation；旧 resourceVersion 写入发生真实 Conflict。
- 使用仓库中的 Role 和受限 ServiceAccount 身份运行 manager：允许 CR 读取、status 更新、Lease 和事件；拒绝 CR 创建/规格更新、Secret/Pod/StatefulSet 读取和跨 namespace 访问。
- 持有真实 leader-election Lease；watch 收敛为 Unknown；重复观察不造成 status churn；另一个 namespace 的资源保持未处理。
- 注入测试专用 Observer 后经历 Unknown → Healthy → 新 generation → Unknown；未知时当前 observation 被清空。
- 测试命名空间没有生成 StatefulSet。

## 尚未验证或实现

- Curvine 版本适配器、实际运维接口、仲裁/数据可用性的业务真实性。
- Pod 调度、镜像运行、PVC 持久化、真实多副本 leader failover、真实 Curvine 集群与其他 Kubernetes 版本。
- 容器镜像构建/发布、用户集群部署和远端 CI 执行。
- 扩缩容执行、drain、升级、备份恢复、MDS/Transfer/CSI 与 Malta 联调。

envtest 的健康数据是测试 fixture，只证明控制器如何处理证据，不能视为 Curvine 应用验收。生产 manager 默认固定使用 Unsupported Observer，没有启用测试健康数据的参数。
