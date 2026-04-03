---
paths: plugin/**/*.go
---
# Plugin 规则

- 插件按职责留在 `plugin/executable`、`plugin/matcher`、`plugin/server`、`plugin/data_provider`、`plugin/switch` 对应目录，不要混层。
- 插件通过 `init()` 注册；改 type 名、参数结构或注册路径时，要考虑配置兼容和迁移提示。
- 可复用逻辑优先抽到 `pkg/`，插件层尽量薄，避免插件之间直接耦合。
- 改插件行为时，至少补当前包测试；若影响注册或配置加载，额外检查 `plugin/enabled_plugin_test.go`、`coremain` 或 `e2e` 回归。
