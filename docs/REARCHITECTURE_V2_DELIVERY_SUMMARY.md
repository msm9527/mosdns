# mosdns V2 重构交付总结

## 1. 文档定位

本文档用于给代码评审、联调和发版说明提供一份浓缩版交付摘要。

如果你只想快速回答下面几个问题，可以优先看这份文档：

- 这轮 V2 重构到底交付了什么？
- 现在运行态真源是什么？
- 前端应该调用哪些 API？
- 运维应该用哪些命令迁移和回滚？

## 2. 已交付的核心变化

### 2.1 运行态真源收口

以下运行态能力已接入 SQLite 主路径：

- 审计日志与摘要
- overrides
- upstreams
- switch
- webinfo
- requery 配置、状态、任务历史、checkpoint
- AdGuard 规则配置
- diversion 规则源配置
- generated datasets / 动态规则导出元数据

### 2.2 稳定核心 API

已建立并落地 `/api/v1/runtime/*` 稳定资源域，重点包括：

- `/api/v1/runtime/summary`
- `/api/v1/runtime/resources`
- `/api/v1/runtime/requery/jobs`
- `/api/v1/runtime/requery/runs`
- `/api/v1/runtime/requery/checkpoints`
- `/api/v1/runtime/requery/enqueue`

### 2.3 CLI 与迁移工具

已提供：

- `mosdns runtime summary`
- `mosdns runtime events`
- `mosdns runtime datasets list/export`
- `mosdns runtime requery jobs|runs|checkpoints`
- `mosdns runtime legacy import/export`
- `mosdns config migrate`
- `mosdns config validate`

### 2.4 config v2

`config v2` 已具备：

- v1 -> v2 一次性迁移
- 最小纯声明式输出
- 编译回当前 plugin graph
- runtime plugin 声明能力

## 3. 真源模型变化

### 改造前

- YAML、JSON、TXT、ndjson 多真源并存
- UI 经常依赖具体插件实例或文件结构
- 动态规则文件既是导出物，又承担真源职责

### 改造后

- 静态真源：`config v2`
- 运行态真源：SQLite
- 文件系统：静态模板、兼容导出物、缓存持久化、导入导出包

## 4. 前端与联调约定

前端现在应优先依赖：

- `/api/v1/runtime/summary`
- `/api/v1/runtime/resources`

而不是：

- 直接读取历史 JSON 文件
- 依赖插件实例 tag
- 假设某个动态规则一定存在某个导出文件

## 5. 兼容策略

当前仍保留兼容闭环：

- `legacy import`：旧 JSON -> SQLite
- `legacy export`：SQLite -> 旧 JSON
- `datasets export`：SQLite -> 兼容 TXT/SRS/规则文件

因此当前阶段属于：

**SQLite 为真源，文件为兼容导出物，旧路径可回滚但不再推荐作为首选读写路径。**

## 6. 建议验收方式

### 开发/联调

1. 启动 mosdns
2. 调用 `/api/v1/runtime/summary`
3. 调用 `/api/v1/runtime/resources`
4. 验证 requery jobs/runs/checkpoints
5. 删除动态规则导出文件后执行 `mosdns runtime datasets export`
6. 执行 `mosdns runtime legacy export` 和 `import` 验证兼容闭环

### 运维/回滚

1. 先执行 `mosdns runtime legacy export`
2. 备份导出的 JSON/TXT 文件
3. 如需临时回退，再切回旧兼容路径

## 7. 当前状态判断

如果满足以下条件，可以认为本轮 V2 主线已经交付：

- 运行态不再以新增 JSON 文件为默认扩展方式
- 新前端可仅依赖稳定 runtime API
- `config v2` 可以迁出并校验
- requery 已具备任务历史和 checkpoint 视图
- 动态规则可从数据库重新导出

## 8. 剩余事项性质

当前剩余项主要属于：

- 兼容期结束后的历史清理
- 更细粒度的 schema 继续规范化
- 非主路径页面/脚本进一步清理旧调用

也就是说，当前不再是“架构未完成”，而是“兼容收尾与长期治理”。
