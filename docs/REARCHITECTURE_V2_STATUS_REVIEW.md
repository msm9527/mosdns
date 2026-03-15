# mosdns V2 重构状态复盘

> 更新时间：2026-03-15
> 分支基线：`dev`

## 1. 文档定位

本文档用于回答三个问题：

1. V2 已经完成了什么。
2. 当前仓库还剩什么收口工作。
3. 新 V2 的系统边界现在是什么。

本文档描述的是当前仓库真实状态，而不是历史方案或迁移计划。

## 2. 当前系统边界

V2 现在按下面的边界运行：

- `config v2`：唯一静态配置入口
- `control.db`：唯一运行态真源
- 文件系统：静态输入、缓存持久化、显式导出物
- 前端：基于现有页面代码重构，但只依赖新 V2 API
- CLI：统一通过 `mosdns control` 与 `mosdns config validate`

不再维护：

- `runtime_kv`
- `旧 runtime import/export`
- `旧迁移命令`
- `runtime resources 聚合接口`
- `/api/v1/control/requery/*` 顶层别名
- `/api/v1/control/clientname` 顶层别名
- `legacy` / `include` / `plugins` 旧配置入口

## 3. 已完成项

### 3.1 runtime 存储收口

运行态结构化表已覆盖：

- switch
- global overrides
- upstream overrides
- webinfo / clientname
- generated datasets
- requery job / run / checkpoint / state
- adguard rule
- diversion rule source
- system events

`runtime_kv` 已从 schema 与代码路径中完全删除。

### 3.2 runtime surface 收口

当前稳定接口集中在：

- `/api/v1/control/summary`
- `/api/v1/control/health`
- `/api/v1/control/datasets`
- `/api/v1/control/datasets/verify`
- `/api/v1/control/datasets/export`
- `/api/v1/control/events`
- `/api/v1/control/clientname`
- `/api/v1/control/requery/*`

CLI 统一到：

- `mosdns control summary|health|events`
- `mosdns control datasets list|verify|export`
- `mosdns control requery jobs|runs|checkpoints|prune`
- `mosdns control shunt explain|conflicts`

### 3.3 config v2 收口

当前配置链路已经是纯 V2：

- `loadConfig` 只接受 `version: 2`
- `legacy` / `include` / `plugins` 会直接报错
- `旧迁移命令` 和迁移实现已删除
- `config validate` 成为唯一配置检查入口

### 3.4 前端与文档收口

- 前端 requery 与 clientname 调用已改到 `/api/v1/control/*`
- 文档主线改为纯 V2 口径
- 运维文档不再描述 legacy 导入导出和回退链路

## 4. 当前仍在做的事情

当前剩余工作主要是收口性质，而不是架构缺口：

- 更新剩余核心文档到纯 V2 口径
- 完成最终全量验证
- 把 task truth artifacts 标记为完成

## 5. 当前结论

V2 主线已经从“重构设计”进入“纯 V2 运行”阶段：

- 运行态模型已稳定到结构化 SQLite
- API surface 已删除关键旧别名
- 配置入口已切断 v1/legacy 路径
- 当前仓库应被视为不承诺升级、不承诺回退的新 V2 系统
