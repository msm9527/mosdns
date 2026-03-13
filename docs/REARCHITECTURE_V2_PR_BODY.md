# mosdns V2 重构 PR 说明模板

以下内容可直接作为本轮 V2 重构 PR 的主体描述使用。

---

## 背景

本次改动围绕 mosdns V2 重构主线展开，目标是把原先分散在 YAML、JSON、TXT、ndjson 和插件内部状态中的运行态真源统一收口，逐步建立以下架构边界：

- `config v2` 作为声明式静态配置入口
- SQLite 作为运行态与动态规则的主真源
- 文件系统退回到静态模板、兼容导出物和缓存持久化角色
- 前端逐步只依赖稳定核心 API，而不是插件实例名或历史 JSON 文件

---

## 本次交付

### 1. 运行态 SQLite 化

已将以下能力纳入运行态 SQLite 主路径：

- 审计日志与摘要
- overrides
- upstreams
- switch
- webinfo
- requery 配置/状态/任务历史/checkpoint
- AdGuard 规则配置
- diversion 规则源配置
- generated datasets / 动态规则元数据

### 2. 稳定核心 API

新增并收口：

- `/api/v1/runtime/summary`
- `/api/v1/runtime/resources`
- `/api/v1/runtime/requery/jobs`
- `/api/v1/runtime/requery/runs`
- `/api/v1/runtime/requery/checkpoints`
- `/api/v1/runtime/requery/enqueue`

并推动内置前端页面切换到稳定 runtime API。

### 3. CLI 与迁移工具

新增或补齐：

- `mosdns runtime summary`
- `mosdns runtime events`
- `mosdns runtime datasets list/export`
- `mosdns runtime requery jobs|runs|checkpoints`
- `mosdns runtime legacy import/export`
- `mosdns config migrate`
- `mosdns config validate`

### 4. config v2

已支持：

- `v1 -> v2` 迁移
- 纯声明式最小输出
- `v2 -> plugin graph` 编译
- runtime plugin 声明能力

---

## 兼容性说明

当前阶段保留兼容闭环，不做激进删除：

- 旧 JSON 状态仍可导入到 SQLite
- SQLite 状态仍可导回 legacy JSON
- 动态规则仍可导出为兼容 TXT/SRS/规则文件
- 历史接口保留兼容期，但新代码应优先使用 `/api/v1/runtime/*`

因此本次变更的兼容策略是：

**SQLite 为真源，文件为导出物，旧路径可兼容回滚但不再建议继续作为首选写入路径。**

---

## 测试

本轮重构涉及模块较多，已分批执行过针对性测试，包括但不限于：

```bash
go test ./coremain ./plugin/switch/switch ./plugin/executable/requery ./internal/configv2
go test ./internal/requeryruntime ./coremain ./plugin/executable/requery ./plugin/executable/domain_output
GOCACHE=$PWD/.tmp/gocache go test ./coremain ./plugin/executable/adguard ./internal/configv2
GOCACHE=$PWD/.tmp/gocache go test ./coremain ./plugin/data_provider/sd_set ./plugin/data_provider/sd_set_light ./plugin/data_provider/si_set
```

文档收尾阶段还执行了：

```bash
git diff --check
```

---

## 风险与回滚

### 主要风险

- `config v1 -> v2` 仍可能存在少量语义边角差异
- 兼容期内数据库与导出文件之间可能存在短暂时间差
- 个别旧页面或外部脚本如果仍依赖历史 JSON 路径，需要逐步切换

### 回滚方式

1. 执行 `mosdns runtime legacy export`
2. 备份导出的 JSON/TXT 文件
3. 如需临时回退，继续使用旧兼容路径
4. 保留现有兼容 API，避免一次性中断 UI 或脚本

---

## 参考文档

- `docs/REARCHITECTURE_V2.md`
- `docs/REARCHITECTURE_V2_IMPLEMENTATION_CHECKLIST.md`
- `docs/REARCHITECTURE_V2_DELIVERY_SUMMARY.md`
- `docs/RUNTIME_V2_OPERATOR_GUIDE.md`
- `docs/API_REFERENCE.md`
