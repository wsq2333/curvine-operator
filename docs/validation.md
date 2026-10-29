# S0 验证记录

本页保留 S0 的验收范围。后续验证见 [S1 验证记录](validation-s1.md) 与 [S2 验证记录](validation-s2.md)。

日期：2026-10-28。环境：macOS arm64，Go 1.26.5。范围是本项目的离线逻辑，没有连接 Kubernetes 或 Curvine 服务。

执行 `make check build` **通过**，覆盖 `go test -race ./...`、`go vet ./...`、gofmt 检查与 CLI 构建。API、CLI、plan 与 tests/acceptance 四个测试包均通过；cmd 入口通过真实进程调用验收。二进制测试会自行构建 CLI，在临时工作目录和不存在的 kubeconfig 路径下运行。

关键测试覆盖：

- 生产单 Master、偶数 Master、可变镜像、错误 digest、隐式存储类、重复池与清除数据策略的拒绝。
- 首次创建计划中 Worker 对应用级仲裁确认步骤的依赖；计划始终不可执行。
- Master 副本、Worker 缩容/移除、存储、镜像与身份变化的结构化阻断。
- 一个请求混合扩容和不支持变更时整体阻断，不产生部分步骤。
- 相同规格不产生步骤；Worker 池顺序不影响计划或哈希；规划不修改输入。
- CLI 未知字段、多 JSON 文档、未知命令、无效参数及退出码 0/2/3。
- 重复键、转义后重复键、大小写错误字段、null、错误类型、整数溢出、非 UTF-8、截断 JSON、1 MiB 大小边界。
- 镜像 registry/端口/路径语法；拒绝空路径段、无效端口、tag 与 URL 形式。
- bootstrap/update 模式、集群身份、新旧规格哈希、新增池与扩容的 from/to 副本数。
- 实际二进制的 11 个验收场景：validate、bootstrap、no-op、scale-out、scale-in、invalid、invalid-previous、missing-file、unknown-command、help、version。
- 帮助命令成功返回且不读取文件；非法规格不产生计划；被阻断的变更返回完整 JSON 且 steps 为空。

使用契约和可复现命令见 [S0 文档](s0.md)。当前变更仅保存在本地，未据此声明远端 CI 通过。

尚未验证：真实 CRD/admission、controller-runtime、资源创建、应用启动与仲裁、磁盘持久化、CSI、MDS/Transfer、升级、drain、备份恢复、Malta 联调和 Linux CI 运行。示例中的镜像和存储类为占位值，离线校验不能证明它们存在。

这份记录证明项目基础能构建且所列静态规则有测试，不构成可部署或生产可用的声明。
