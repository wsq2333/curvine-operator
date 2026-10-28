# Curvine Operator

面向 Kubernetes 的 Curvine 集群生命周期管理项目：把集群规格、运行状态、数据安全约束和运维操作收敛到可观察、可恢复的控制循环。

**当前阶段：S0 项目基础与设计原型。** 已实现 Go 规格模型、离线校验、变更计划、操作保护规则和测试。尚未实现 Kubernetes controller、CRD 安装、实际部署或在线运维；目前不能作为可部署 Operator 使用。项目与 `malta`、`curvine` 同级，独立维护。

## 已实现

| 能力 | 当前行为 |
| --- | --- |
| 集群规格 | `CurvineCluster` 的 v1alpha1 草案；区分开发/生产，描述镜像、Master 和持久化 Worker 池 |
| 校验 | 镜像 digest、显式存储类、生产 3/5 Master、唯一 Worker 池、默认数据保留策略；拒绝未知 JSON 字段 |
| 离线计划 | 首次创建、规格差异、Worker 增加与扩容意图；输出确定性 JSON 与规格哈希 |
| 变更保护 | Master 副本变更、Worker 缩容/移除、镜像升级、存储变更和身份迁移返回结构化阻断原因 |
| 验证 | 单元测试、CLI 契约测试、race、vet；CI 配置已提供，尚未触发远端运行 |

`plan` 不读取 kubeconfig，不连接 Kubernetes，不执行任何步骤。`executable` 始终为 `false`。`--previous` 是用户提供的旧规格，不等于线上当前状态；`production` 只表示规格校验档位，不表示项目或示例通过了生产验收。

## 本地使用

需要 Go 1.26.5，与当前同级 Malta 项目的开发工具版本一致。S0 仅依赖标准库。Go 模块路径为 `github.com/wsq2333/curvine-operator`。

```sh
make check build
bin/curvine-operator validate --file examples/cluster-development.json
bin/curvine-operator plan --file examples/cluster-production.json
bin/curvine-operator plan --file desired.json --previous previous-spec.json
```

最后一条命令需要自行准备新旧规格文件。退出码：`0` 为规格有效/无静态阻断的建议计划，`2` 为输入错误，`3` 为变更被阻断。无阻断不代表在线操作安全。示例镜像 digest 和 StorageClass 是占位值，只供离线演示；没有可直接 `kubectl apply` 的 CRD 或部署清单。

## 能力建设方向

1. **P0：集群生命周期与数据安全。** Master 持久化与固定身份、Worker 存储和拓扑、真实健康状态、幂等 reconcile、资源所有权、数据保留。
2. **P1：受控运维。** Worker drain 后缩容、逐节点升级、备份恢复演练、凭据与可观测性、可中断恢复的操作记录。
3. **P2：完整服务与数据接入。** MDS/FoundationDB、Transfer、服务发现、CSI/FUSE 集成，以及与 Fluid 的明确管理边界。
4. **P3：有证据的优化。** 容量和热点驱动的建议式扩容、数据预热、通过 Malta 做同环境性能对照。自动执行需建立在 P0/P1 验收之上。

详见 [能力设计](docs/design.md)、[源码依据与待确认接口](docs/source-audit.md)、[分阶段路线与验收](docs/roadmap.md)、[验证记录](docs/validation.md)。

## 项目结构

```text
api/v1alpha1/             当前离线规格模型；未来演进为 Kubernetes API types
cmd/curvine-operator/     CLI 入口
internal/cli/            JSON 输入、输出与退出码
internal/plan/           纯函数规划器和操作保护规则
examples/                开发/生产意图示例，均不可直接部署
docs/                    设计、证据、实施路线和验证边界
.github/workflows/       本地逻辑的 CI 检查
```

下一阶段新增 `cmd/manager`、`internal/controller`、`internal/observe`、`internal/actuate` 和 `config/crd`。控制器按“读取实际状态 → 规划 → 执行有限步骤 → 更新状态 → 重试”工作，复用经过测试的领域约束。**不要把 S0 计划直接接到执行器上**：线上规划必须加入 UID、generation、所有权、健康观察和持久化操作状态。
