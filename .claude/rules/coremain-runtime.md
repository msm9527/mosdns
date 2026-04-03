---
paths: coremain/**/*.go
---
# Core Runtime 规则

- `coremain/` 负责启动、配置装配、运行时 API、热重载和服务生命周期；纯工具能力优先下沉到 `pkg/` 或 `internal/`。
- 改配置编译、热重载、运行时 API、HTTP handler 时，同步检查文档、样例配置和对应 `*_test.go`。
- 错误返回优先保留上下文，使用 `%w` 包装。
- 改跨模块装配时，先梳理边界，不要让 `coremain` 直接承担过多底层实现细节。
