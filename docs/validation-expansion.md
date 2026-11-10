# Kubernetes 读缓存扩容验证

日期：2026-11-04。本次在独立 kind 集群执行，未使用既有 kubeconfig context、业务集群、用户 Secret 或生产来源。机器可读证据：[两轮验证结果](validation-expansion-results.json)。

## 环境与制品

- kind v0.31.0 / Kubernetes v1.35.0，Linux arm64，单个 Kubernetes 节点，单 Master；Worker 从 1 扩到 3，再扩到 4。
- StorageClass `standard`（kind local-path），每个 StatefulSet 使用 1 GiB PVC 申请。local-path 不代表真实云盘容量配额或多节点存储语义。
- Curvine v0.5.4-alpha 固定源码 `89df141912e96d127421de6e4d36e6e2190d9949`，此前构建的 debug server/FUSE/SDK helper；本次加入当前 Go node agent 与 manager。
- 运行镜像本地 index digest：`sha256:b68ea44e7f5a33f3a2bb41a2a7480a1c91cf6ea5e589c9b19d93d950cc3eb6bb`。
- 最终 Operator 镜像本地 image ID：`sha256:4fb5995dcf7c2dc70c27777e1e1bee3d44a8e90d3d913f29e2d48d53a6909b12`。以上是本地验收制品，不是已发布可拉取的发行镜像。
- 来源为仓库只读 S3 fixture。每轮使用 96 个不同的冷对象，单对象 25 字节；第二轮使用新对象名，避免已有缓存影响新 Worker 落盘证据。

## 实测结果

| 场景 | 结果 |
| --- | --- |
| Kubernetes 真实部署 | Master、Worker、挂载 Job、FUSE DaemonSet 均正常启动；Job Completed；应用 Pod init 通过真实 FUSE ro/文件摘要验证 |
| 1 → 3 扩容 + Operator 重启 | 35 轮业务 FUSE 摘要校验，0 次失败；两个新增 Worker 各有 750 / 950 字节写入及读取增长 |
| 3 → 4 扩容 + Operator 重启 | 22 轮业务 FUSE 摘要校验，0 次失败；新增 Worker 写入及读取各增长 575 字节；显式 rollout status 成功 |
| 状态收敛 | generation 3，desired/created/scheduled/ready/volumesBound/registered 均为 4，phase=Registered，stalled=false |
| 进度诊断 | 真实采样记录 WaitingForScheduling、WaitingForPods、Registered；其他阻断/超时分支由单元测试覆盖 |
| 指标端点 | 通过 API proxy 读取 Operator Service、Worker Pod、FUSE Pod 的 /metrics，核对所有新增状态指标及原生容量/读写/延迟系列 |

本次首先以最终 Go Operator 验证 1→3；之后补强验收脚本的 manifest 校验与显式 rollout 完成检查，再执行 3→4。第一轮 JSON 不包含后加的 operatorRolloutComplete 字段；该轮新 Operator Pod 已实际启动，第二轮由工具显式验证。

这些都是小对象的功能验收，证明新增 Worker 参与了数据路径，不能推导生产吞吐或扩容后均衡程度。持续探针是约一秒采样，不保证采样间隙零中断。所有 Worker 位于一个 Kubernetes 节点，不证明跨节点、跨故障域或 Master HA。未验收真实 OSS/S3 IAM、TLS/STS、FUSE/节点重启、节点掉线、容量压力、保留卷恢复或全量数据一致性。

## 自动检查

- `make generate`：通过，CRD/DeepCopy 包含 WorkerPoolProgress 与 allocationSampleTime。
- `make check build`：通过，全包 race、vet、格式检查、Python manifest 验证与三个 Go 二进制构建。
- `make test-envtest`：通过，最终 15.143s，独立 Kubernetes 1.36.0 API Server/etcd。沙箱内无法解析 localhost 的首次尝试未启动控制面；允许本地临时监听后重跑成功。
- `kubectl kustomize config/monitoring`：通过，并已安装到专用 kind 集群。
- Prometheus v3.2.1 `promtool check rules`：8 条规则通过；`check config --syntax-only`：抓取配置通过。未部署常驻 Prometheus/Grafana，未进行完整告警时间窗口演练。

## 复现准备

扩容命令、manifest 与权限要求见 [扩容工具说明](worker-expansion.md)。工具只要求一个已健康的测试读缓存集群，不依赖此次私有临时目录。

如需使用本地 kind + fixture：

1. 按 [首期验证记录](validation-read-cache.md) 在独立 checkout 构建固定上游的 server/FUSE/SDK helper，以及对应架构的 Go manager/cache-node。
2. 将五个二进制（curvine-server、curvine-fuse、curvine-cache-mount、curvine-cache-node、manager）及 `tests/e2e/s3_fixture.py` 放进临时构建目录，使用 `tests/e2e/Dockerfile.scale-out` 构建本地组合验收镜像。该文件使用前述 `curvine-readcache-toolchain:local` 基础镜像。
3. 通过 `kind create cluster --name <独立名称> --kubeconfig <独立路径>` 创建专用集群，将镜像加载进去。CR image 必须使用真实 digest；kind containerd 需要相应的 digest 名称别名，不能以虚构 digest 替代。确认专用节点存在 `/dev/fuse`，并仅对该节点添加 `storage.curvine.io/fuse=enabled` 标签。
4. 应用 `config/monitoring`，将 manager 镜像替换为组合镜像、command 设为 `/manager`。如缩短 poll-interval，observation-timeout 不能大于它；本次均为 2s。
5. 在测试 namespace 启动 `python3 /s3_fixture.py` 的 origin Pod 和指向 8080 的 Service。使用 fixture 固定测试凭据（见 fixture 源码）创建测试 Secret。以 `examples/cluster-read-cache.json` 创建 CR，替换实际 digest、development/单 Master、standard PVC、单 Worker、origin endpoint。保留不可变来源和探针配置。
6. fixture 提供 `scale-<数字>.txt`，内容与探针相同。生成 96 个对象的 manifest；第二次扩容应换一组未读过的数字。由活体 CR/受管 ConfigMap 渲染 consumer，应用镜像使用含 Python 的验收镜像；运行长驻 Python 主进程以供 exec 验证。
7. 待 ResourcesReady、WorkersHealthy、ReadPathVerified 均为 True 后运行工具。结束后仅删除自己的独立 kind 集群；不要对实际业务集群运行清理命令。

测试使用特权 FUSE Pod 与双向挂载传播；真实部署仍需满足集群准入、节点及网络约束。来源 fixture 不验证完整 SigV4 签名。以上验证不改变缩容/升级保护，也不补造上游缺失的 quorum 证明。
