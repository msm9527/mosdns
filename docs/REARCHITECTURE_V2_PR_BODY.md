# mosdns V2 重构 PR 说明模板

以下内容可直接作为纯 V2 重构 PR 的主体描述使用。

---

## 背景

本次改动围绕 mosdns 全新 V2 主线展开，目标是把原先分散在 YAML、JSON、TXT、ndjson 和插件内部状态中的配置与运行态模型收口为：

- `config v2` 作为唯一配置入口
- `control.db` 作为唯一运行态真源
- 文件系统只保留静态输入、缓存持久化与显式导出物角色
- 前端统一依赖新 V2 API

---

## 本次交付

### 1. 运行态存储

- 删除 `runtime_kv`
- 新增 webinfo / requery 结构化表
- 删除 旧 runtime import/export 相关实现与测试
- 清理 runtime store 中的 legacy fallback 逻辑

### 2. runtime surface

- 删除 `runtime resources 聚合接口`
- 删除 `/api/v1/control/requery/*` 顶层别名
- 删除 `/api/v1/control/clientname` 顶层别名
- 前端调用统一到 `/api/v1/control/*`

### 3. config v2

- 删除 `旧迁移命令`
- 删除迁移实现与 legacy block
- `loadConfig` 只接受 v2 文档
- 旧键 `legacy` / `include` / `plugins` 会显式报错

### 4. 文档与验证

- 核心文档改为纯 V2 口径
- 运维文档改为新 runtime API / CLI
- 全量测试通过后收口本轮重构

---

## 测试

建议至少执行：

```bash
go test ./...
```

如本轮包含文档与资源更新，可补充：

```bash
git diff --check
```

---

## 影响说明

本轮改动属于直接切换到新 V2：

- 不再维护 legacy runtime 导入导出
- 不再维护 `旧迁移命令`
- 不再保留已删除的 runtime surface 别名
- 文档和前端都按纯 V2 口径工作

---

## 参考文档

- `docs/REARCHITECTURE_V2.md`
- `docs/REARCHITECTURE_V2_IMPLEMENTATION_CHECKLIST.md`
- `docs/REARCHITECTURE_V2_DELIVERY_SUMMARY.md`
- `docs/RUNTIME_V2_OPERATOR_GUIDE.md`
- `docs/API_REFERENCE.md`
