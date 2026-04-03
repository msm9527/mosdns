本文档只保留这个目录真正需要补充的规则；通用安全、非交互执行和工程规范以系统级规则为准。
## Harness
- 这个仓库里的 harness 指的是一整套执行外壳：`AGENTS.md`、`CLAUDE.md`、`.claude/rules/`、`.claude/scripts/`、本地 hooks、测试与 CI；不是单独某个神秘 agent。
- Claude 可以直接消费 `.claude/settings.local.json` 的 hooks；Codex 不会自动执行这些 hooks，但必须主动读取并复用同一套规则与验证脚本。
- 如当前终端已启用全局 `rtk` hook，沿用全局 RTK 约定即可，不在仓库内重复定义。

## 项目结构约束

- `main.go` 只保留版本注入、blank import 和 `coremain.Run()` 启动；新 CLI 子命令优先放 `tools/`，再通过 `coremain.AddSubCmd` 注册。
- `coremain/` 负责启动、配置装配、运行时 API、热重载、生命周期编排；可复用的通用能力不要堆在这里，优先下沉到 `pkg/` 或 `internal/`。
- `plugin/` 按插件职责分目录：`executable/`、`matcher/`、`server/`、`data_provider/`、`switch/`；插件通过 `init()` 注册，类型名和参数结构改动要考虑配置兼容与迁移提示。
- `pkg/` 保持通用库属性，避免反向依赖 `coremain` 或具体插件实现。
- `internal/` 只放项目内部机制，例如 `configv2`、runtime store；对外行为变化要同步文档与回归测试。
- `config/ui/` 是嵌入式静态资源，改这里后至少验证 `go build ./...`，避免 `go:embed` 或资源引用回归。

## 工作流

### 1. Plan

- 先读 `CLAUDE.md`、相关目录规则和已有测试，确认改动落点。
- 变更跨 `coremain`、`plugin`、`pkg` 多层时，先拆清责任边界，再动手。

### 2. Execute

- 配置解析、插件注册、运行时 API 这三类改动优先保持向后兼容；确实要破坏兼容时，同步补迁移提示或文档。
- 改导出 API、插件 type、配置字段、HTTP 接口时，同步搜索调用点、示例配置、文档和测试。

### 3. Verify

- 单包改动：先跑 `.claude/scripts/verify-go-package.sh <file>`。
- 跨包或提交前：跑 `.claude/scripts/verify-repo.sh`。
- 涉及 `e2e/`、`config/ui/`、运行时 API、配置编译器时，不能只跑单包测试，至少补一次仓库级验证。
