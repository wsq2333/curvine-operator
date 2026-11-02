# v0.5.4-alpha 契约验证记录

日期：2026-11-02。基线：Operator `f91dcdb`。Curvine 固定源码：`89df141912e96d127421de6e4d36e6e2190d9949`（v0.5.4-alpha）。本轮只修改 Operator。

| 检查 | 结果与证据范围 |
| --- | --- |
| `make generate` | 通过；重新生成 CRD 和 DeepCopy |
| `make check build` | 通过；全包 race 测试、CLI 验收、vet、gofmt、两个二进制构建 |
| `make test-envtest` | 最终版本通过（12.690s）；临时 Kubernetes 1.36.0 API Server/etcd，不使用现有集群 |
| `kubectl kustomize config/workloads` | 通过；渲染包含 `--workload-contract=curvine-v0.5.4-alpha` |

新增覆盖：

- 固定源码对应的 HTTP JSON 字段、Master clusterID/地址、唯一 Active、缺失 Master、切主与多个 Active。
- Worker 实际注册、心跳陈旧/未来、Decommission 混入 live 数组、错误构建来源、legacy 缺少结构化版本、缺失会话、失败磁盘、未注册与外部 Worker。
- Content-Type、HTML 回退页、重复键及转义重复键、超大响应、尾随 JSON、字段缺失/类型错误、重定向、取消超时、非法 Target 不发请求。
- 发布契约与实际版本分离；未知能力不得升级为 Healthy；新旧运行契约冲突阻断；样本必须匹配资源引擎选定的契约及源码。
- 真实 HTTP 测试服务器 → Observer → Controller → 临时 Kubernetes API，验证新增 status schema、namespace Role 权限、generation 更新、HTTP 失败后清除 observation。
- 在 HTTP 报告 Active 的情况下，实际生成 Master 及支持资源，但不生成 Worker StatefulSet，明确报告 `QuorumObservationUnsupported`。

HTTP 数据是按固定源码构造的测试 fixture，非真实 Curvine 响应采集。envtest 没有 kubelet、调度、CSI 或实际 Raft/Worker 进程；没有执行 Curvine 镜像构建/启动、真实文件 I/O、卷恢复、性能或故障注入。没有向用户集群部署，也没有 commit/push。

S1/S2 仍待完成的实机验收及上游缺失接口详见 [应用契约](contracts/v0.5.4-alpha.md)。
