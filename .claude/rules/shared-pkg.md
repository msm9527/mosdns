---
paths: pkg/**/*.go
---
# Shared Package 规则

- `pkg/` 是通用库层，避免反向依赖 `coremain` 或具体插件实现。
- 改导出类型、函数、错误语义前，先搜索调用点并补测试。
- 网络、并发、缓存相关改动先保证 correctness，再做性能优化；关闭资源、处理 deadline 和并发安全要显式。
- 性能敏感路径优先复用现有池化、matcher、transport 设施，不要随意引入新的全局状态。
