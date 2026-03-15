# mosdns V2 重构实施清单

## 1. 文档定位

本文档记录全新 V2 重构的执行清单与完成状态。

当前仓库遵循的实施原则只有三条：

- 直接建设纯 V2，不维护迁移主线
- 运行态只认 `runtime.db`
- 配置入口只认 `config v2`

## 2. 已完成的实施项

### 2.1 runtime 存储

- [x] 删除 `runtime_kv`
- [x] 为 webinfo / requery 建立结构化表
- [x] 删除 runtime store 中的 legacy fallback 逻辑
- [x] 删除 旧 runtime import/export 命令、实现与测试

### 2.2 runtime API / CLI / 前端

- [x] 删除 `runtime resources 聚合接口`
- [x] 删除 `/api/v1/runtime/requery/*` 顶层别名
- [x] 删除 `/api/v1/runtime/clientname` 顶层别名
- [x] 前端改用 `/api/v1/runtime/requery/*` 与 `/api/v1/runtime/clientname`
- [x] runtime CLI 收口到 summary / health / datasets / events / requery / shunt

### 2.3 config v2

- [x] 删除 `旧迁移命令`
- [x] 删除 `internal/configv2/migrate.go`
- [x] 删除 `legacy` block 类型与编译路径
- [x] 显式拒绝 `legacy` / `include` / `plugins` 旧键
- [x] `loadConfig` 只接受 v2 文档

### 2.4 文档与验证

- [x] 更新核心架构文档到纯 V2 口径
- [x] 更新运维文档到新 runtime API / CLI
- [x] 更新 PR 模板与交付总结
- [ ] 执行最终全量验证
- [ ] 收口 task truth artifacts

## 3. 稳定验证项

完成收口前必须通过：

```bash
go test ./...
```

同时应确认：

- 文档不再引用已删除的 `runtime resources 聚合接口`
- 文档不再引用已删除的 `/api/v1/runtime/requery/*` 顶层路径
- 文档不再引用已删除的 `/api/v1/runtime/clientname` 顶层路径
- 文档不再引用 `旧迁移命令`

## 4. 当前结论

V2 已不是阶段性迁移工程，而是仓库当前唯一有效主线。
