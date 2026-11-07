# 内存优化与版本替换

本分支服务于公司内部的 Codex/OpenAI 请求，沿用 Sub2API 0.2.19 的功能、接口、PostgreSQL、Redis 和数据模型。源码基线为 `dece26153621959ccd7635814f4b2d08304e681a`。

## 长会话为何放大内存

2026 年 11 月 6 日的现场记录中，6 位用户半小时提交 531 个请求，25 个输入超过 100MiB，最大约 112MiB。已关联请求的重叠峰值为 14 次。大输入进入网关后，整段复制和跨请求持有会继续放大占用。

原版受控采样累计分配约 10.83GiB，其中约 4.53GiB 来自 OAuth 输入元数据清理。旧实现每删除一条历史消息的内部字段，就复制整份请求；50 条历史消息会产生约 50 份请求大小的累计分配。

## 当前实现

1. 模型名、会话标识等短字段拥有自己的字符串。原始请求字节的零拷贝视图限于同步读取阶段。
2. 异步用量任务在提交前保存模型、渠道、定价时刻和请求标识，任务只持有所需的计量数据与余额预留释放引用。
3. OpenCode 协议转换前保存经过校验的小型会话提示，沿用原有会话来源优先级。
4. OAuth 元数据清理逐项修改目标消息，其他消息保留原始 JSON，最后集中合成完整请求。
5. 会话压缩标记检查直接读取输入项；普通历史复用原字节，压缩请求集中重排一次。重复键与特殊输入继续采用原有完整解码语义。
6. 内容审计的空队列只预分配任务引用，完整任务在实际入队时分配，保持原有队列容量和处理行为。

## 性能对照

测试环境：Apple M1 Pro、Go 1.27.3、`GOMAXPROCS=2`。下面的内存数字为一次处理累计分配，进程常驻内存由线上采样另行记录。

| 场景 | 原版分配 | 当前分配 | 原版耗时 | 当前耗时 |
| --- | ---: | ---: | ---: | ---: |
| 16MiB 图片＋50 条元数据 | 856,202,112B | 16,797,968B | 258.07ms | 20.74ms |
| 112MiB 图片＋50 条元数据 | 5,990,028,920B | 117,461,264B | 1.734s | 142.21ms |

该清理步骤累计分配下降约 98.04%。回归检查同时覆盖图片内容、原始输入、消息顺序、重复字段、转义字段、大整数和异步用量归因。

普通 16MiB 图片请求执行完整 Codex 转换和流式响应处理时，累计分配从 218,443,080B 降至 134,536,736B，约下降 38.4%。内容审计服务初始化当前分配 803,360B；原版空队列的 100,000 个完整任务槽约占 28.8MB。

默认请求模型、服务和处理器测试全部通过；带 `unit` 标签的会话压缩、OAuth 元数据、内容审计与风险控制回归通过。独立审查已核对本次全部源码差异。

使用项目现有 Go 工具链复测：

```sh
cd backend
go test -p 1 ./internal/pkg/requestmodel ./internal/service ./internal/handler
go test -p 1 -tags=unit ./internal/service -run 'Test.*Compaction|TestHasCompaction'
go test -p 1 ./internal/service -run '^$' -bench 'BenchmarkOpenAIOAuthInputMetadata|BenchmarkOpenAICodexLargeRequest|BenchmarkContentModerationIdleService' -benchtime=1x -benchmem
```

## 构建与运行验证

前端使用锁定依赖和项目脚本构建，后端通过 `embed` 构建标签嵌入同源前端。正式制品包含版本、提交和构建时间。

```sh
cd frontend
pnpm install --frozen-lockfile
pnpm run build
cd ../backend
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -p 1 -tags embed -trimpath -o bin/sub2api ./cmd/server
```

在原服务器保存当前应用镜像、Compose、环境、应用配置和数据库快照。共享原数据库和 Redis 的验证实例按单应用实例顺序启动，继续由原 Caddy 的 `sub2api` 服务别名接入。受控采样入口绑定容器回环地址，正式制品使用常规构建标签。

验证包含流式和普通 Responses 请求、用量写入、首页与健康检查、容器重启计数、内存与交换空间，以及系统等待内存回收的时间。高负载对照使用同尺寸合成请求，真实 API 用小型请求核对完整服务链。

恢复时切回操作前保存的应用镜像和运行配置，保留切换期间生成的业务数据。数据库与 Redis 使用既有实例和结构。
