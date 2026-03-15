# mosdns V2 架构说明

## 1. 文档定位

本文档定义当前仓库采用的 V2 架构边界。

这不是迁移蓝图，而是现行实现的目标模型：

- `config v2` 是唯一配置入口
- `runtime.db` 是唯一运行态真源
- 文件系统只负责静态输入、缓存持久化和显式导出物
- 前端与外部调用统一依赖稳定 V2 API

## 2. 为什么要这样做

旧模型的问题主要集中在三点：

1. 配置表达依赖 `include` 顺序、插件 tag 和历史约定。
2. 运行态散落在多个 JSON/TXT 文件中，边界不清晰。
3. 前端、脚本和插件内部实现耦合过深，难以演进。

V2 的目标是把这些问题收敛为清晰边界，而不是继续叠加补丁层。

## 3. 系统边界

### 3.1 Config Plane

`config v2` 负责声明：

- listeners
- upstreams
- policies / routing
- rule providers
- runtime features

当前实现中：

- 只接受 `version: 2`
- 拒绝 `legacy` / `include` / `plugins` 旧键
- 通过纯 V2 compiler 生成当前执行链路

### 3.2 Runtime Plane

`runtime.db` 负责：

- switch 状态
- overrides
- upstream overrides
- webinfo / clientname
- generated datasets 元数据
- requery job / run / checkpoint / state
- adguard rule
- diversion rule source
- system events

运行态数据只能从数据库读取和治理，不从历史 JSON 反向回填。

### 3.3 Data Plane

当前 DNS 数据面仍基于现有 plugin / sequence 执行内核。

V2 对数据面的要求是：

- 保留现有性能路径
- 配置与运行态改造不破坏执行语义
- 管理面不再把插件内部状态暴露为外部契约

### 3.4 Management Plane

管理面统一通过：

- CLI：`mosdns runtime`、`mosdns config validate`
- HTTP API：`/api/v1/runtime/*` 与现有业务资源域
- 前端：基于现有页面重构，但只消费 V2 API

## 4. API 边界

当前运行态稳定接口为：

- `/api/v1/runtime/summary`
- `/api/v1/runtime/health`
- `/api/v1/runtime/datasets`
- `/api/v1/runtime/datasets/verify`
- `/api/v1/runtime/datasets/export`
- `/api/v1/runtime/events`
- `/api/v1/runtime/clientname`
- `/api/v1/runtime/requery/*`

已删除的旧 surface：

- `runtime resources 聚合接口`
- `/api/v1/runtime/requery/*`
- `/api/v1/runtime/clientname`

## 5. CLI 边界

当前 CLI 主线为：

```bash
mosdns runtime summary
mosdns runtime health
mosdns runtime events
mosdns runtime datasets list
mosdns runtime datasets verify
mosdns runtime datasets export
mosdns runtime requery jobs
mosdns runtime requery runs
mosdns runtime requery checkpoints
mosdns runtime requery prune
mosdns runtime shunt explain
mosdns runtime shunt conflicts
mosdns config validate
```

已删除：

- `mosdns 旧迁移命令`
- `mosdns 旧 runtime import`
- `mosdns 旧 runtime export`

## 6. 文件系统角色

文件系统只保留三类职责：

- 静态配置和规则源
- 显式导出的数据文件
- 缓存 dump / WAL 等执行面持久化物

文件不是运行态真源，也不是配置迁移入口。

## 7. 验证要求

V2 收口必须持续满足：

```bash
go test ./...
```

并且代码、文档、前端引用保持同一口径。
