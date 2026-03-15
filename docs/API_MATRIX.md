# API 接口矩阵

## 1. 文档定位

本文档用于列出当前项目中主要可见接口，并按稳定级别分类：

- `stable`：建议前端和外部调用优先使用
- `compat`：仍可用，但不建议新代码继续依赖
- `internal`：内部/插件层接口，不保证长期稳定

本文档重点用于后续接口治理，不追求逐字段展开所有请求体。

## 2. 分级标准

### 2.1 stable

满足以下条件之一：

- 已挂在 `/api/v1/*` 或 `/api/v2/*`
- 已有明确业务语义
- 前端可长期稳定依赖

### 2.2 compat

满足以下情况之一：

- 仍有调用价值，但设计不够理想
- 为旧前端或旧脚本保留
- 存在更推荐的替代接口

### 2.3 internal

满足以下情况之一：

- 直接暴露插件实例名
- 更偏调试、插件运维或底层能力
- 不适合作为外部稳定 API

## 3. 核心 API 矩阵

## 3.1 观测与调试

| 方法 | 路径 | 模块 | 等级 | 说明 |
| --- | --- | --- | --- | --- |
| `GET` | `/metrics` | 观测 | `stable` | Prometheus 指标 |
| `GET` | `/debug/pprof/*` | 调试 | `internal` | 运行时分析入口 |

## 3.2 日志抓取

| 方法 | 路径 | 模块 | 等级 | 说明 |
| --- | --- | --- | --- | --- |
| `POST` | `/api/v1/capture/start` | 运行日志抓取 | `stable` | 开始抓取临时日志 |
| `GET` | `/api/v1/capture/logs` | 运行日志抓取 | `stable` | 获取本次抓取日志 |

## 3.3 审计

| 方法 | 路径 | 模块 | 等级 | 说明 |
| --- | --- | --- | --- | --- |
| `POST` | `/api/v1/audit/start` | 审计 | `stable` | 开始采集 |
| `POST` | `/api/v1/audit/stop` | 审计 | `stable` | 停止采集 |
| `GET` | `/api/v1/audit/status` | 审计 | `stable` | 获取采集状态 |
| `GET` | `/api/v1/audit/logs` | 审计 | `compat` | v1 日志获取，建议逐步弱化 |
| `POST` | `/api/v1/audit/clear` | 审计 | `stable` | 清空审计数据 |
| `GET` | `/api/v1/audit/capacity` | 审计存储 | `stable` | 获取审计存储设置 |
| `POST` | `/api/v1/audit/capacity` | 审计存储 | `stable` | 更新审计存储设置 |
| `GET` | `/api/v2/audit/stats` | 审计 | `stable` | 概览统计 |
| `GET` | `/api/v2/audit/rank/domain` | 审计 | `stable` | 域名排行 |
| `GET` | `/api/v2/audit/rank/client` | 审计 | `stable` | 客户端排行 |
| `GET` | `/api/v2/audit/rank/domain_set` | 审计 | `stable` | 规则集排行 |
| `GET` | `/api/v2/audit/rank/slowest` | 审计 | `stable` | 慢查询排行 |
| `GET` | `/api/v2/audit/logs` | 审计 | `stable` | 分页日志查询 |

## 3.4 运行态聚合

| 方法 | 路径 | 模块 | 等级 | 说明 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/runtime/summary` | 运行态聚合 | `stable` | 获取 SQLite 运行态摘要与命名空间概览 |
| `GET` | `/api/v1/runtime/health` | 运行态聚合 | `stable` | 获取运行态自检结果 |
| `GET` | `/api/v1/runtime/datasets` | 运行态聚合 | `stable` | 获取 generated datasets 列表 |
| `POST` | `/api/v1/runtime/datasets/export` | 运行态聚合 | `stable` | 导出 generated datasets 到文件 |
| `POST` | `/api/v1/runtime/datasets/verify` | 运行态聚合 | `stable` | 校验 generated datasets 与文件一致性 |
| `GET` | `/api/v1/runtime/events` | 运行态聚合 | `stable` | 获取 runtime system events |
| `GET` | `/api/v1/runtime/requery/jobs` | requery 任务 | `stable` | 获取任务定义 |
| `GET` | `/api/v1/runtime/requery/runs` | requery 任务 | `stable` | 获取最近运行历史 |
| `GET` | `/api/v1/runtime/requery/checkpoints` | requery 任务 | `stable` | 获取 checkpoint，可按 `run_id` 过滤 |
| `POST` | `/api/v1/runtime/requery/enqueue` | requery 任务 | `stable` | 触发运行态 requery 入队 |

## 3.5 覆盖配置

| 方法 | 路径 | 模块 | 等级 | 说明 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/overrides/` | 覆盖配置 | `stable` | 读取全局覆盖配置 |
| `POST` | `/api/v1/overrides/` | 覆盖配置 | `stable` | 保存并应用全局覆盖配置 |

## 3.6 配置管理

| 方法 | 路径 | 模块 | 等级 | 说明 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/config/info` | 配置管理 | `stable` | 获取当前工作目录和远程源 |
| `POST` | `/api/v1/config/export` | 配置管理 | `stable` | 导出当前配置 |
| `POST` | `/api/v1/config/update_from_url` | 配置管理 | `stable` | 拉取远程配置并覆盖更新 |

## 3.7 更新

| 方法 | 路径 | 模块 | 等级 | 说明 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/update/status` | 更新 | `stable` | 获取更新状态 |
| `POST` | `/api/v1/update/check` | 更新 | `stable` | 强制检查 |
| `POST` | `/api/v1/update/apply` | 更新 | `stable` | 执行更新 |

## 3.8 系统

| 方法 | 路径 | 模块 | 等级 | 说明 |
| --- | --- | --- | --- | --- |
| `POST` | `/api/v1/system/restart` | 系统 | `stable` | 计划自重启 |

## 3.9 上游配置

| 方法 | 路径 | 模块 | 等级 | 说明 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/upstream/tags` | 上游配置 | `stable` | 获取插件 tag 列表 |
| `GET` | `/api/v1/upstream/config` | 上游配置 | `stable` | 获取完整配置 |
| `PUT` | `/api/v1/upstream/config` | 上游配置 | `stable` | 替换配置并可直接应用 |
| `POST` | `/api/v1/upstream/apply` | 上游配置 | `stable` | 应用当前已保存配置 |
| `GET` | `/api/v1/upstream/items` | 上游配置 | `stable` | 按插件分组查询 |
| `POST` | `/api/v1/upstream/items` | 上游配置 | `stable` | 新增上游项 |
| `PUT` | `/api/v1/upstream/items/{upstreamTag}` | 上游配置 | `stable` | 更新上游项 |
| `DELETE` | `/api/v1/upstream/items/{upstreamTag}` | 上游配置 | `stable` | 删除上游项 |
| `POST` | `/api/v1/upstream/config` | 上游配置 | `compat` | 旧兼容入口，建议弱化 |

## 3.10 运行时聚合统计

| 方法 | 路径 | 模块 | 等级 | 说明 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/cache/stats` | 运行时统计 | `stable` | 聚合缓存统计 |
| `GET` | `/api/v1/data/domain_stats` | 运行时统计 | `stable` | 聚合域名统计 |

## 4. 插件 API 矩阵

## 4.1 开关管理

### 核心接口

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/switches` | `stable` | 获取全部开关 |
| `GET` | `/api/v1/switches/{name}` | `stable` | 获取单开关 |
| `PUT` | `/api/v1/switches/{name}` | `stable` | 更新单开关 |

说明：

- `switches` 旧插件接口已移除
- 开关能力只保留 `/api/v1/switches/*`

## 4.2 分流刷新（requery）

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/runtime/requery/summary` | `stable` | 聚合状态与配置 |
| `GET` | `/api/v1/runtime/requery` | `stable` | 配置读取 |
| `GET` | `/api/v1/runtime/requery/status` | `stable` | 运行状态 |
| `POST` | `/api/v1/runtime/requery/trigger` | `stable` | 触发任务 |
| `POST` | `/api/v1/runtime/requery/enqueue` | `stable` | 入队按需刷新 |
| `POST` | `/api/v1/runtime/requery/cancel` | `stable` | 取消任务 |
| `POST` | `/api/v1/runtime/requery/scheduler/config` | `stable` | 更新调度配置 |
| `POST` | `/api/v1/runtime/requery/rules/save` | `stable` | 批量保存分流规则 |
| `POST` | `/api/v1/runtime/requery/rules/flush` | `stable` | 批量清空分流规则 |
| `GET` | `/api/v1/runtime/requery/stats/source_file_counts` | `stable` | 刷新源统计 |

建议：

- 旧的 `/plugins/requery/*` 与 `/api/v1/runtime/requery/*` 已移除，只保留 `/api/v1/runtime/requery/*`

## 4.3 缓存插件

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/cache/stats` | `stable` | 聚合缓存统计 |
| `GET` | `/api/v1/cache/{tag}/stats` | `stable` | 单缓存实例统计 |
| `GET` | `/api/v1/cache/{tag}/entries` | `stable` | 查看缓存内容，统一 JSON 返回结构 |
| `POST` | `/api/v1/cache/{tag}/save` | `stable` | 持久化单缓存实例 |
| `POST` | `/api/v1/cache/{tag}/flush` | `stable` | 清空缓存 |
| `POST` | `/api/v1/cache/{tag}/purge_domain` | `stable` | 按域名清缓存 |

建议：

- 旧的 `/plugins/{cache_tag}/*` 已移除
- UI 和兼容页面统一走 `/api/v1/cache/*`

## 4.4 记忆库/规则集数据接口

适用插件包括：

- `domain_set_light`
- `ip_set`
- `rewrite`
- 部分 `domain_output`

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/lists/{tag}` | `stable` | 查看可编辑列表内容 |
| `PUT` | `/api/v1/lists/{tag}` | `stable` | 替换可编辑列表内容 |
| `GET` | `/api/v1/memory/{tag}/entries` | `stable` | 查看分流记忆库内容 |
| `POST` | `/api/v1/memory/{tag}/save` | `stable` | 保存分流记忆库 |
| `POST` | `/api/v1/memory/{tag}/flush` | `stable` | 清空分流记忆库 |
| `POST` | `/api/v1/memory/{tag}/verify` | `stable` | 按域名验证/发布 |
| `GET` | `/api/v1/memory/{tag}/stats` | `stable` | 某些记忆库统计 |

问题：

- 可编辑列表已完成核心 API 收口
- `domain_output` 已完成核心 API 收口

## 4.5 在线分流规则源

适用类型包括：

- `geositecn`
- `geositenocn`
- `geoipcn`
- `cuscn`
- `cusnocn`

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/rules/diversion` | `stable` | 聚合获取所有规则源 |
| `PUT` | `/api/v1/rules/diversion/{type}/{name}` | `stable` | 新增/更新规则源 |
| `DELETE` | `/api/v1/rules/diversion/{type}/{name}` | `stable` | 删除规则源 |
| `POST` | `/api/v1/rules/diversion/{type}/{name}/update` | `stable` | 后台更新指定规则源 |

建议：

- 旧的插件规则源接口已移除
- 统一只保留 `/api/v1/rules/diversion/*`

## 4.6 AdGuard 规则

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/rules/adguard` | `stable` | 获取规则列表 |
| `POST` | `/api/v1/rules/adguard` | `stable` | 新增规则 |
| `PUT` | `/api/v1/rules/adguard/{id}` | `stable` | 更新规则 |
| `DELETE` | `/api/v1/rules/adguard/{id}` | `stable` | 删除规则 |
| `POST` | `/api/v1/rules/adguard/update` | `stable` | 后台更新启用规则 |

建议：

- 旧的 AdGuard 插件接口已移除
- 统一只保留 `/api/v1/rules/adguard/*`

## 4.7 其他插件接口

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/runtime/clientname` | `stable` | 获取客户端别名配置 |
| `PUT` | `/api/v1/runtime/clientname` | `stable` | 更新客户端别名配置 |
| `GET` | `/api/v1/switches/core_mode` | `stable` | 当前核心模式 |
| `GET` | `/api/v1/reverse_lookup?ip=...` | `stable` | 反查 IP 对应域名 |

说明：

- 旧的 `/plugins/clientname` 已移除
- 旧的 `/plugins/reverse_lookup` 已移除
- `webinfo` 作为内部文件持久化插件，不再暴露独立 HTTP API

## 5. 主要问题标注矩阵

| 问题类型 | 典型接口 | 说明 |
| --- | --- | --- |
| `GET` 带副作用 | 历史 `/plugins/{tag}/flush` `/plugins/{tag}/save` | 当前代码基线已移除 |
| 返回纯文本 | 部分插件操作接口 | 不利于统一错误处理 |
| 强耦合插件 tag | 历史 `/plugins/geosite_cn/*` | 说明过往前端曾绑定实现细节 |
| 业务与插件边界重叠 | 少量历史说明仍提到插件层 | 核心 API 基本已收口 |
| 版本边界缺失 | 历史 `/plugins/*` | 这类路径已不再作为当前主接口层 |

## 6. 推荐迁移优先级

### P0：先补文档标识

优先在文档中明确：

- `stable`
- `compat`
- `internal`

### P1：UI 高频能力先收口

优先建议补核心 API 的模块：

1. `switches`
2. `requery`
3. `rules`
4. `cache detail actions`

### P2：统一返回结构

先从新增和核心接口开始：

- 所有操作接口统一 JSON
- 所有分页接口统一结构

### P3：淘汰历史副作用 GET 和 `/post`

前提：

- 新接口已经替代完成
- UI 已迁移

## 7. 最终建议

当前接口面可以归纳成一句话：

- `/api/*` 已经形成了主接口层
- 历史 `/plugins/*` 问题已经基本收口出主链路

后续治理的正确方向是：

- 继续让业务资源收口到 `/api/*`
- 仅把 `/plugins/*` 保留在历史说明里，不再恢复通用插件路由层
- 用文档和分级约束后续新增接口，不再继续扩散历史风格
