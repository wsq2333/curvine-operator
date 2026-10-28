# S0 验证记录

日期：2026-10-28。环境：macOS arm64，Go 1.26.5。范围是本项目的离线逻辑，没有连接 Kubernetes 或 Curvine 服务。

执行 `make check build`，覆盖 `go test -race ./...`、`go vet ./...`、gofmt 检查与 CLI 构建。使用开发、生产示例运行 validate/plan，并验证相同新旧规格得到空步骤计划。

关键测试覆盖：

- 生产单 Master、偶数 Master、可变镜像、错误 digest、隐式存储类、重复池与清除数据策略的拒绝。
- 首次创建计划中 Worker 对应用级仲裁确认步骤的依赖；计划始终不可执行。
- Master 副本、Worker 缩容/移除、存储、镜像与身份变化的结构化阻断。
- 一个请求混合扩容和不支持变更时整体阻断，不产生部分步骤。
- 相同规格不产生步骤；Worker 池顺序不影响计划或哈希；规划不修改输入。
- CLI 未知字段、多 JSON 文档、未知命令、无效参数及退出码 0/2/3。

尚未验证：真实 CRD/admission、controller-runtime、资源创建、应用启动与仲裁、磁盘持久化、CSI、MDS/Transfer、升级、drain、备份恢复、Malta 联调和 Linux CI 运行。示例中的镜像和存储类为占位值，离线校验不能证明它们存在。

这份记录证明项目基础能构建且所列静态规则有测试，不构成可部署或生产可用的声明。
