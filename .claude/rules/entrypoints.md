---
paths: main.go
---
# Entry Point 规则

- `main.go` 只保留版本信息、blank import 和 `coremain.Run()`；不要把业务逻辑直接塞进入口。
- 新 CLI 子命令优先放 `tools/`，再通过 `coremain.AddSubCmd` 注册。
- 新增 blank import 时，确认对应包依赖 `init()` 完成注册，并补相关测试或启动验证。
