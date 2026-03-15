# mosdns V2 重构交付总结

## 1. 文档定位

本文档用于给代码评审、联调和发版说明提供一份简明交付摘要。

当前仓库已经按全新 V2 主线收口：

- `config v2` 是唯一配置入口
- `runtime.db` 是唯一运行态真源
- 文件只承担静态输入、缓存持久化和显式导出物角色
- 前端与外部调用统一走新 V2 API

## 2. 已交付能力

### 2.1 运行态真源

以下运行态数据已进入 SQLite 结构化存储：

- overrides
- upstreams
- switch
- webinfo / clientname
- requery 配置、状态、job、run、checkpoint
- AdGuard 规则配置
- diversion 规则源配置
- generated datasets
- system events

### 2.2 稳定 API

当前前端和外部调用应使用以下接口：

- `GET /api/v1/runtime/summary`
- `GET /api/v1/runtime/health`
- `GET /api/v1/runtime/datasets`
- `POST /api/v1/runtime/datasets/verify`
- `POST /api/v1/runtime/datasets/export`
- `GET /api/v1/runtime/events`
- `GET /api/v1/runtime/clientname`
- `PUT /api/v1/runtime/clientname`
- `/api/v1/runtime/requery/*`

旧的 `runtime resources 聚合接口`、`/api/v1/runtime/requery/*`、`/api/v1/runtime/clientname` 顶层别名已移除。

### 2.3 CLI

当前运维命令集已收口到：

- `mosdns runtime summary`
- `mosdns runtime health`
- `mosdns runtime events`
- `mosdns runtime datasets list|verify|export`
- `mosdns runtime requery jobs|runs|checkpoints|prune`
- `mosdns runtime shunt explain|conflicts`
- `mosdns config validate`

`旧迁移命令` 与 `旧 runtime import/export` 已删除。

### 2.4 config v2

`config v2` 已切为唯一配置主线：

- `loadConfig` 只接受 v2 文档
- `legacy` / `include` / `plugins` 旧键会被显式拒绝
- 编译链仅服务于纯 V2 schema

## 3. 真源模型

### 改造后

- 静态真源：`config v2`
- 运行态真源：`runtime.db`
- 导出文件：由数据库或配置显式生成，不反向驱动运行态

## 4. 联调约定

前端和脚本应：

- 通过 `/api/v1/runtime/summary` 获取概览
- 通过 `/api/v1/runtime/health`、`/datasets`、`/events` 获取运行态细分信息
- 通过 `/api/v1/runtime/requery/*` 获取任务状态与执行控制
- 通过 `/api/v1/runtime/clientname` 读写 webinfo clientname

不再依赖：

- 历史 JSON 文件
- 顶层别名接口
- 旧配置迁移链路

## 5. 建议验收方式

1. 启动 mosdns 并加载 v2 配置
2. 调用 `/api/v1/runtime/summary`
3. 调用 `/api/v1/runtime/health`
4. 调用 `/api/v1/runtime/datasets` 与 `/api/v1/runtime/events`
5. 验证 `/api/v1/runtime/requery/*` 返回正常
6. 删除动态规则导出文件后执行 `mosdns runtime datasets export`
7. 执行 `go test ./...`

## 6. 当前结论

本轮不再是“边做边兼容”的过渡架构，而是已经切到纯 V2 主线：

- 代码路径不再维护 legacy runtime 导入导出
- 配置入口不再接受 v1 风格文档
- runtime API 不再保留已移除的别名 surface
- 文档、CLI、前端与测试统一指向新 V2 模型
