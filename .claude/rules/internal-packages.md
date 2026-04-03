---
paths: internal/**/*.go
---
# Internal Package 规则

- `internal/` 只放项目内部机制，例如配置编译、runtime store、持久化；对外行为变化要同步文档和回归测试。
- `internal/configv2` 改动要同时检查配置兼容、样例配置和编译器测试。
- `internal/store` 改动要关注持久化格式、并发安全和启动恢复路径。
