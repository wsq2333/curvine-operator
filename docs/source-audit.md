# Curvine 源码核查与接口缺口

核查日期：2026-10-28。本地 Curvine 路径为 `../curvine/curvine`，HEAD 为 `728d2a46`，核查时工作区无修改。以下判断针对该本地版本，不等于上游发布镜像已包含这些能力。

没有可用的 CodeGraph MCP 或本地 `.codegraph` 索引，依照 Curvine 的 `cv-codegraph` 指引使用直接源码读取。未安装索引工具，未修改 Curvine 或 Malta。

## 已观察的事实与影响

| 事实 | 本地源码依据 | 对 Operator 的影响 |
| --- | --- | --- |
| 已有 Master StatefulSet、Worker Deployment 和 Service 示例 | [部署示例目录](../../curvine/curvine/curvine-docker/deploy/example) | 可参考启动命令和端口，不能原样当作通用部署模板 |
| Master 示例未挂载元数据/journal PVC；Worker 示例含固定云节点标签、hostNetwork、privileged 和 hostPath | 同上 `curvine-master.yaml`、`curvine-worker.yaml` | 把持久化、节点拓扑和权限变成明确策略，消除环境硬编码 |
| Master 用 8995 作 readiness，8996 作 startup/liveness，注释解释快照恢复阶段 | [Master 示例](../../curvine/curvine/curvine-docker/deploy/example/curvine-master.yaml) | 区分进程存活与服务就绪；TCP 探针仍不足以代表仲裁/数据安全 |
| 示例 `journal_addrs` / `master_addrs` 的名字未表达完整 StatefulSet service/namespace 结构 | [配置示例](../../curvine/curvine/curvine-docker/deploy/example/conf/curvine-cluster.toml) | 统一生成 DNS 和 Raft ID；不能把示例主机名当作已验证的部署契约 |
| `check_master_hostname` 检查自身 hostname 是否在 journal_addrs；指定 NIC 时按解析 IP 和端口检查 | [cluster_conf.rs](../../curvine/curvine/crates/common/curvine-config/src/cluster_conf.rs) | 启动前校验成员地址与本机身份；不能混用 Pod FQDN 和自动解析 IP |
| Worker CLI 已调用 `/add-dcm`、`/remove-dcm`，服务侧 add_dcm 将 WorkerStatus 标为 Decommission | [node.rs](../../curvine/curvine/curvine-cli/src/cmds/node.rs)、[worker_manager.rs](../../curvine/curvine/curvine-master/src/master/fs/worker_manager.rs) | 已有退役入口；观察到的函数不提供“所有数据已撤离”的证明，需要进一步核验完整协议 |
| Worker 心跳处理保留 Decommission 状态 | [worker_map.rs](../../curvine/curvine/curvine-master/src/master/fs/state/worker_map.rs) | 有退役状态保持逻辑，但控制器仍需验证重启、leader 切换和回滚语义 |
| Raft 存在 `handle_conf_change` / `apply_config_change` 和 AddNode/RemoveNode | [raft_node.rs](../../curvine/curvine/crates/metadata/curvine-raft/src/raft/raft_node.rs) | 不把成员变更判为完全不存在；需要确认可调用的外部接口、追赶条件与幂等契约 |
| 配置存在 `format_master` / `format_worker`、Worker `dir_reserved` / `free_ratio` | [配置示例](../../curvine/curvine/curvine-docker/deploy/example/conf/curvine-cluster.toml) | 格式化必须是独立初始化动作；空间保留与写准入应纳入容量和缩容判断 |
| MDS 可独立启动，配置含 fdb/memory backend 和 fdb_cluster_file | [mds_conf.rs](../../curvine/curvine/crates/common/curvine-config/src/mds_conf.rs)、[entrypoint.sh](../../curvine/curvine/curvine-docker/deploy/entrypoint.sh) | MDS 不能仅作为普通 Master 副本；生产需引用 FDB 并验证事务健康 |
| Transfer 有 memory/sqlite/mysql/postgres 相关配置、instance_id、lease 和 endpoints | [transfer_conf.rs](../../curvine/curvine/crates/common/curvine-config/src/transfer_conf.rs) | 副本数与任务状态后端耦合，不假定 Transfer 无状态，也不宣称所有后端已验收 |
| 服务发现示例以 etcd 为首期 provider，注释说明首期未包含 auth/TLS | [discovery 配置](../../curvine/curvine/curvine-docker/deploy/example/conf/curvine-cluster.toml) | 初期只在已明确边界的网络内集成，安全能力列为版本依赖 |
| 已有独立 Go CSI 驱动、FUSE 管理和 mount state 逻辑 | [CSI 源码](../../curvine/curvine/curvine-csi/pkg/csi) | Operator 负责安装/兼容和配置引用，不接管 NodePublishVolume 或复制挂载状态机 |
| 容器入口可检测并跳转 Fluid runtime | [entrypoint.sh](../../curvine/curvine/curvine-docker/deploy/entrypoint.sh) | 必须明确 Fluid 与本 Operator 的资源所有权模式 |
| Malta 已定义环境/版本快照与基准运行、对照能力 | [Malta README](../../malta/README.md) | 可复用其验证结果契约；实际联调仍在路线图中 |

## 优先确认的上游契约

1. **健康与发现：** 哪个稳定接口返回 leader、仲裁、成员追赶、Worker 注册和版本？返回结果的时效、错误码、认证和超时语义是什么？
2. **退役：** add-dcm 是否只禁止新分配？谁执行数据迁移？怎样查询在途写、必需数据、欠副本和持久化完成？证明能否绑定 Worker incarnation、操作 ID 和一致性点？
3. **Raft：** 对外成员变更入口、节点身份映射、learner/追赶语义、leader 迁移，以及重复请求和半成功后的恢复办法。
4. **持久化：** 镜像中 metadata、journal、Worker 数据路径与 UID/GID 契约；空盘首次初始化、已有盘恢复与误挂盘拒绝行为。
5. **备份：** 一致性快照导出/恢复接口及数据覆盖范围；内部 Raft snapshot 与可保管的灾备制品之间的差异。
6. **配置升级：** 热更新白名单、需要重启的字段、跨版本元数据兼容、可降级区间与镜像能力声明。
7. **MDS/Transfer：** 客户端路由模式、FDB 引用、Transfer HA 后端和租约接管的实际验收状态。

在这些契约稳定前，用适配层隔离版本差异，未知能力返回 `UnsupportedCapability` / `Unknown`，不通过猜测 HTTP 路径或解释日志文本来执行破坏性操作。
