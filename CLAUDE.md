# mosdns - Agent 项目上下文

> 本文件是项目级 harness 的简版入口，给 Claude/Codex 共用。保持精简，细节交给 `AGENTS.md`、`.claude/rules/` 和仓库文档。

## 构建 & 验证

```bash
go build -o ./bin/mosdns .
go build -gcflags "all=-N -l" -o ./bin/mosdns-debug .
go test ./...
go test ./e2e -count=1
go vet ./...
.claude/scripts/verify-repo.sh
```

## 架构地图

```text
main.go                 ← 启动入口；只做版本注入、blank import、Run
coremain/               ← 启动流程、配置装配、runtime API、热重载、服务编排
plugin/
  executable/           ← 可执行 DNS 插件
  matcher/              ← 匹配器插件
  server/               ← 各类服务端监听插件
  data_provider/        ← 数据源插件
  switch/               ← 切换控制插件
pkg/                    ← 通用库（网络、缓存、上游、matcher、server 等）
internal/               ← 项目内部机制（configv2、runtime store）
tools/                  ← CLI 子命令，通过 coremain.AddSubCmd 注册
config/ui/              ← 嵌入二进制的静态 UI 资源
e2e/                    ← 端到端与报告回归测试
docs/                   ← 设计、开发、迁移与 API 文档
```

## 关键约定

- 新 CLI 能力优先加到 `tools/`，不要把命令解析直接塞进 `main.go`。
- 插件改动要同时关注注册、配置兼容、文档和测试。
- 可复用逻辑先放 `pkg/`，项目内部编排放 `coremain/` 或 `internal/`。
- 改 `config/ui/`、配置字段、运行时 API、热重载链路时，至少跑一次仓库级验证。
- commit 说明必须中英双语。
