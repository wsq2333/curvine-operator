# 读缓存首期验证记录

后续 Kubernetes 扩容与挂载传播验证见 [2026-11-04 记录](validation-expansion.md)；下文保留首期验收时的范围。

日期：2026-11-02。在已有 v0.5.4-alpha S1/S2 改动之上实现，不修改原 Curvine/Malta 仓库。固定上游源码 commit：`89df141912e96d127421de6e4d36e6e2190d9949`。

## 本地控制面与构建

| 检查 | 结果 |
| --- | --- |
| `make generate` | 通过；CRD 与 DeepCopy 包含不可变 readCache 规格 |
| `make check build` | 通过；全包 race、CLI 验收、vet、gofmt，构建 Operator、manager、cache-node |
| `make test-envtest` | 通过，最终测试 14.534s；独立 Kubernetes 1.36.0 API Server/etcd |
| SDK helper + 固定版本 server/FUSE | Linux arm64 debug 构建通过；S3/OSS feature 均启用，最终 `cargo build --locked` 通过 |
| 业务 Pod 渲染器 | 校验合法生成、只读传播、不带 Secret，以及拒绝错误 ConfigMap 所有者 |

控制面新增测试覆盖：只读规格校验，禁止 URL/字段内联凭据，配置不可变，Master 启动后分配 Worker，Mount Job 完成前不创建 FUSE，扩容不改挂载 Job 或 FUSE 模板，仍拒绝缩容，manager 无 Secret 权限。节点/Observer 单元测试覆盖真实文件长度/摘要、符号链接拒绝、FUSE ro 挂载信息、过期/错误身份报告、缺少命中证据保持 Unknown，以及来源失败但读路径成功的独立状态。

envtest 不含调度器、kubelet、CSI 或真实 FUSE。故上述状态更新使用测试 fixture，不能证明 Kubernetes 上实际挂载传播或存储恢复。

## 真实进程数据路径

使用隔离 Docker Linux arm64 网络，运行固定源码构建的 Master、Worker、FUSE、SDK helper 与 Linux Go node agent。输入配置由生产 workload builder 生成。底层是仓库内可注入故障的只读 S3 协议 fixture；测试凭据固定为 fixture 值，不使用现有集群、用户 Secret 或生产数据。

最终运行全部通过，14 项断言均为 true。机器可读结果见 [验证结果](validation-read-cache-results.json)。

| 场景 | 验收依据 |
| --- | --- |
| 幂等挂载（通过） | 连续两次 configure 成功，check 核对同一注册 |
| 错误凭据/凭据漂移（通过） | origin 拒绝错误 access key，check 拒绝与已有注册不一致的 Secret 内容 |
| 冷读（通过） | FUSE 读取探针 SHA256/长度正确、miss counter 增长、来源 GET 增长 |
| 热读（通过） | 再次读文件成功且 FUSE cache-hit counter 与 Curvine 实读字节增长 |
| 只读边界（通过） | 新建、覆盖、删除、重命名、mkdir 均被拒绝，来源写请求总数为 0 |
| 来源中断（通过） | 已缓存探针仍能通过真实 FUSE 读取与摘要校验 |
| Worker 停止（通过） | 探针回源读取和摘要校验成功，来源 GET 增长；新节点报告 readVerified=true、cacheHitObserved=false |
| 来源内容改变（通过） | 同长度不同内容的探针被摘要校验拒绝 |

上游 hit 计数发生在选择缓存 reader 时，随后仍可能回源。本次额外验证 Curvine 成功读取字节；单元测试拒绝仅 hit 增长而无实读字节的“缓存健康”，实机验证 Worker 停止后保持该边界。

S3 fixture 不验证完整 SigV4 签名；上述坏凭据用例不是云 IAM 验收。OSS 目前只验证配置映射、规格和 feature 编译，未连接真实 OSS。集群仅单 Master/单 Worker，不能推导 HA、quorum 或所有文件的一致性保证。

测试先以 128 MiB tmpfs 作为 Worker 数据盘，来源读成功但异步缓存填充被拒绝：扣除空闲空间后不足以分配默认 128 MiB block。改用 512 MiB tmpfs 后观察到真实热命中。这个故障说明 `ReadPathVerified` 与 `CacheReady` 必须分开，也说明容量门槛需要以块大小和预留空间计算，不能仅看探针对象大小。

## 复现入口

1. 用 `helpers/cache-mount/Dockerfile.toolchain` 构建本地工具链镜像。
2. 在独立临时目录 checkout 固定上游 commit，执行 `python3 scripts/prepare-cache-helper.py DISPOSABLE_CHECKOUT`。该命令修改传入 checkout 的 workspace/lockfile 并复制 helper，不应指向工作中的 Curvine 仓库。
3. 在该工具链容器中构建，target 使用独立 Docker volume（测试默认 `curvine-readcache-target`）：

```sh
cargo build --locked -p curvine-cache-mount -p curvine-server -p curvine-fuse \
  --features curvine-fuse/opendal-s3,curvine-fuse/opendal-oss,curvine-data-transfer/opendal-oss
```

4. 构建与 Docker 架构匹配的 Linux Go node agent，生成独立测试配置，再启动验收驱动。例如本次使用 arm64：

```sh
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/curvine-cache-node ./cmd/cache-node
mkdir -p /tmp/curvine-cache-fixtures
CURVINE_E2E_OUTPUT=/tmp/curvine-cache-fixtures go test -tags=e2e -count=1 ./tests/e2e -run TestRender
python3 tests/e2e/docker_read_cache.py \
  --fixtures /tmp/curvine-cache-fixtures --node-binary /tmp/curvine-cache-node \
  --output /tmp/curvine-cache-result/results.json
```

驱动创建唯一命名的网络/容器，FUSE 容器使用特权和 `/dev/fuse`；结束清理容器/网络，保留构建缓存与结果。失败时原始日志存放在结果目录，权限 0600；上游 Master 日志可能包含 fixture 凭据。异常进程终止时需人工清理该次 `cv-cache-` 前缀资源，不能批量清理其他测试资源。

## 尚未完成的部署验收

- 组合发行镜像 `Dockerfile.readcache` 的完整 release 镜像构建、digest 发布和拉取；本次验证的是独立 debug 二进制。
- 真实 S3/OSS endpoint、IAM 权限、TLS/STS 和来源限流；STS/自动凭据轮换尚未实现。
- Kubernetes 特权准入、hostPath/双向挂载传播、业务 Pod 接入与节点重启/残留挂载恢复。
- 多 Master/Worker、网络分区、跨故障域、Operator/FUSE 重启、CR 删除后的业务协调和保留卷恢复。
- TTL 到期的完整驱逐/回源实测、容量压力/欠副本、总回源限流、吞吐/延迟/长期命中率。

本地探针只验证指定不可变对象。未知证据不会升级为全量数据健康；缩容、升级和 Master 成员变更仍阻断。本轮不部署到现有 Kubernetes 集群，不 commit/push。
