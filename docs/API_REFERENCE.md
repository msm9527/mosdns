# mosdns API 文档（当前项目版）

> 本文档以当前仓库源码为准，整理前端与运维实际会用到的 API。
> 重点保留“获取状态 / 更新配置 / 触发更新 / 重启系统”这类请求。
> 当前对外标准接口以 `/api/v1/*` 与 `/api/v2/*` 为准。
> 文档里出现的 `/plugins/*` 仅用于历史兼容说明，不再作为通用插件注册约定。
> 文档中的稳定级别说明：
>
> - `stable`：推荐前端和外部调用优先使用
> - `compat`：兼容接口，仍可用，但不建议新代码继续依赖
> - `internal`：内部/插件层接口，不保证长期稳定

## 0. 当前稳定接口优先顺序

- 配置与系统：优先使用 `/api/v1/*`
- 开关：优先使用 `/api/v1/control/switches/*`
- 上游与规则：优先使用 `/api/v1/upstream/*`、`/api/v1/rules/*`
- 缓存：优先使用 `/api/v1/cache/*`
- 分流记忆库：优先使用 `/api/v1/memory/*`
- 可编辑列表：优先使用 `/api/v1/lists/*`
- `clientname` / `reverse_lookup`：优先使用 `/api/v1/control/clientname`、`/api/v1/reverse_lookup`

## 1. 基础约定

- 默认根地址：`http://127.0.0.1:9099`
- CORS：允许 `GET, POST, PUT, DELETE, OPTIONS`
- 返回格式不是完全统一的：
  - 核心 API 多数返回 JSON
  - 很多插件 API 返回 `text/plain`
  - 少数接口返回二进制，如缓存 dump、配置导出 zip
- 查询类插件接口常见参数：
  - `q`：后端搜索
  - `limit`：返回条数
  - `offset`：偏移量

### 1.1 阅读顺序建议

- `2.x 核心 API`：优先按 `stable` 使用
- `3.x / 4.x 插件接口`：大多属于 `compat` 或 `internal`
- 若同一能力同时存在 `/api/*` 和历史 `/plugins/*`
  - 优先使用 `/api/*`
  - `/plugins/*` 仅视为兼容遗留路径，不建议新代码继续依赖

## 2. 核心 API

### 2.1 观测与调试

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/metrics` | `stable` | Prometheus 指标 |
| `GET` | `/debug/pprof/*` | `internal` | Go pprof 调试入口 |

### 2.2 日志抓取

稳定级别：`stable`

#### `POST /api/v1/capture/start`

开始临时抓取运行日志，并把日志级别提升到 `DEBUG`。

请求体可选：

```json
{
  "duration_seconds": 120
}
```

- 默认值：`120`
- 合法范围：`1 ~ 600`

#### `GET /api/v1/capture/logs`

获取抓取到的日志，返回 JSON 数组。

### 2.3 运行态聚合 API

根路径：`/api/v1/control`

| 方法 | 子路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/summary` | `stable` | 获取运行态命名空间概览、存储引擎和聚合摘要 |
| `GET` | `/health` | `stable` | 获取运行态自检结果 |
| `GET` | `/datasets` | `stable` | 获取 generated datasets 列表 |
| `POST` | `/datasets/export` | `stable` | 导出 generated datasets 到文件 |
| `POST` | `/datasets/verify` | `stable` | 校验 generated datasets 与文件一致性 |
| `GET` | `/events` | `stable` | 获取 runtime system events |
| `GET` | `/requery/jobs` | `stable` | 获取 requery 任务定义 |
| `GET` | `/requery/runs` | `stable` | 获取最近 requery 运行历史 |
| `GET` | `/requery/checkpoints` | `stable` | 获取 checkpoint，可按 `run_id` 过滤 |
| `POST` | `/requery/enqueue` | `stable` | 触发 requery 任务入队 |

`GET /api/v1/control/summary` 重点用于：

- 前端首页或诊断页做统一状态拉取
- 确认当前运行态是否已经由 SQLite 接管
- 查看 `switch / webinfo / requery / adguard_rule / diversion_rule / generated_dataset` 是否有数据

说明：

- `generated_dataset` 属于动态规则导出真源，对应文件通过显式 export 动作生成。
- 运行态页面应按模块分别调用 `/summary`、`/health`、`/datasets`、`/events` 与 `/runtime/requery/*`，不再依赖单一大聚合接口。

### 2.4 审计 API v1

根路径：`/api/v1/audit`

| 方法 | 子路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `POST` | `/start` | `stable` | 开始审计采集 |
| `POST` | `/stop` | `stable` | 停止审计采集 |
| `GET` | `/status` | `stable` | 获取采集状态 |
| `GET` | `/logs` | `compat` | 获取审计日志，建议逐步转向 v2 查询接口 |
| `POST` | `/clear` | `stable` | 清空审计日志 |
| `GET` | `/capacity` | `stable` | 获取审计存储设置 |
| `POST` | `/capacity` | `stable` | 设置审计存储设置 |

示例：

```json
{
  "capturing": true
}
```

```json
{
  "memory_entries": 100000,
  "current_memory_entries": 527,
  "retention_days": 30,
  "max_disk_size_mb": 10,
  "current_disk_size_bytes": 1048576,
  "capacity": 100000
}
```

设置审计存储请求体：

```json
{
  "memory_entries": 100000,
  "retention_days": 30,
  "max_disk_size_mb": 10
}
```

说明：

- `memory_entries`：内存窗口条数，用于 UI 快速查询。
- `current_memory_entries`：当前实际保存在内存窗口里的日志条数。
- `retention_days`：磁盘日志保留天数。
- `max_disk_size_mb`：磁盘日志总占用上限。
- `capacity`：兼容旧前端，等同于 `memory_entries`。
- `POST /clear`：会同时清空内存窗口和已落盘的审计历史。
- `POST /capacity`：设置保存后立即生效，无需重启服务。

### 2.5 审计 API v2

根路径：`/api/v2/audit`

| 方法 | 子路径 | 等级 | 说明 | 常用参数 |
| --- | --- | --- | --- | --- |
| `GET` | `/stats` | `stable` | 获取总请求数与平均耗时 | 无 |
| `GET` | `/rank/domain` | `stable` | 域名排行 | `limit` |
| `GET` | `/rank/client` | `stable` | 客户端排行 | `limit` |
| `GET` | `/rank/domain_set` | `stable` | 规则集排行 | `limit` |
| `GET` | `/rank/slowest` | `stable` | 慢查询排行 | `limit` |
| `GET` | `/logs` | `stable` | 分页日志查询 | `page,limit,domain,answer_ip,cname,client_ip,q,exact` |

`/logs` 返回结构：

```json
{
  "pagination": {
    "total_items": 0,
    "total_pages": 0,
    "current_page": 1,
    "items_per_page": 50
  },
  "logs": []
}
```

### 2.6 覆盖配置

根路径：`/api/v1/overrides`

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/` | `stable` | 获取覆盖配置与替换规则命中结果 |
| `POST` | `/` | `stable` | 保存覆盖配置 |

请求体示例：

```json
{
  "socks5": "127.0.0.1:1080",
  "ecs": "1.2.3.4",
  "replacements": [
    {
      "original": "old-value",
      "new": "new-value",
      "comment": "example"
    }
  ]
}
```

### 2.7 配置管理

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/config/info` | `stable` | 获取当前运行中的配置目录、默认远程源和覆盖提醒 |
| `POST` | `/api/v1/config/export` | `stable` | 导出当前运行中的配置目录为 zip |
| `POST` | `/api/v1/config/update_from_url` | `stable` | 从远程配置源拉取配置覆盖当前目录并触发重启 |

`GET /api/v1/config/info` 返回示例：

```json
{
  "dir": "/path/to/current/config",
  "remote_source": "https://github.com/msm9527/mosdns/tree/main/config",
  "warning": "远程更新会覆盖所有配置，请提前备份。"
}
```

`POST /api/v1/config/export` 请求体：

```json
{
  "dir": "/path/to/mosdns"
}
```

说明：

- `dir` 可省略。
- 省略时自动使用当前运行中的 `mosdns` 工作目录。

`POST /api/v1/config/update_from_url` 请求体：

```json
{
  "url": "https://github.com/msm9527/mosdns/tree/main/config"
}
```

支持的 `url` 类型：

- GitHub `tree` 地址  
  例如：`https://github.com/msm9527/mosdns/tree/main/config`
- 直接可下载的 `zip` 地址  
  例如：`https://example.com/mosdns-config.zip`

更新行为：

- 请求体中的 `dir` 可省略，默认使用当前运行中的配置目录。
- 如果 `url` 为空，默认使用：
  - `https://github.com/msm9527/mosdns/tree/main/config`
- 当 `url` 为 GitHub `tree` 地址时：
  - 后端会自动转换为对应仓库分支的源码归档下载地址
  - 然后只提取 `tree` 指向的子目录内容进行覆盖
- 当 `url` 为 `zip` 地址时：
  - 后端直接下载该 zip
  - 如果 zip 只有一层顶级包裹目录，会自动剥离该目录后再覆盖
- 更新前会先把当前配置完整备份到工作目录下的 `backup/`
- 更新失败会回滚已覆盖文件
- 更新成功后会自动重启 `mosdns`

成功返回示例：

```json
{
  "message": "配置更新成功，已覆盖 12 个文件。MosDNS 即将自动重启。",
  "status": "success"
}
```

### 2.8 版本检查与程序更新

根路径：`/api/v1/update`

| 方法 | 子路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/status` | `stable` | 获取更新状态 |
| `POST` | `/check` | `stable` | 强制重新检查更新 |
| `POST` | `/apply` | `stable` | 应用更新 |

`POST /api/v1/update/apply` 请求体：

```json
{
  "force": false,
  "prefer_v3": false
}
```

常见返回字段：

- `current_version`
- `latest_version`
- `architecture`
- `release_url`
- `download_url`
- `update_available`
- `pending_restart`
- `checked_at`

### 2.8 运行时聚合统计

这组接口用于减少前端请求次数，并统一缓存访问入口。

- 缓存管理页不再分别请求多个 `/plugins/{cache_tag}/stats`
- 域名统计卡片不再分别请求多个 `/plugins/.../show?limit=1`
- 前端改为各调用 1 次聚合接口

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/cache/stats` | `stable` | 一次性获取所有缓存实例统计 |
| `GET` | `/api/v1/cache/{tag}/stats` | `stable` | 获取单缓存实例统计 |
| `GET` | `/api/v1/cache/{tag}/entries` | `stable` | 查看单缓存实例内容，返回结构化 JSON |
| `POST` | `/api/v1/cache/{tag}/save` | `stable` | 持久化单缓存实例到磁盘 |
| `POST` | `/api/v1/cache/{tag}/flush` | `stable` | 清空单缓存实例 |
| `POST` | `/api/v1/cache/{tag}/purge_domain` | `stable` | 按域名精确清理缓存 |
| `GET` | `/api/v1/data/domain_stats` | `stable` | 一次性获取所有域名统计实例计数 |

`GET /api/v1/cache/stats` 返回示例：

```json
{
  "items": [
    {
      "key": "cache_all",
      "name": "全部缓存 (兼容)",
      "tag": "cache_all",
      "snapshot_file": "cache/cache_all.dump",
      "wal_file": "cache/cache_all.wal",
      "backend_size": 0,
      "l1_size": 0,
      "updated_keys": 0,
      "counters": {
        "query_total": 0,
        "hit_total": 0,
        "l1_hit_total": 0,
        "l2_hit_total": 0,
        "lazy_hit_total": 0,
        "lazy_update_total": 0,
        "lazy_update_dropped_total": 0
      },
      "last_dump": {
        "status": "ok",
        "at": "2026-03-12T13:51:25+08:00",
        "duration": "8ms",
        "entries": 0,
        "error": ""
      },
      "last_load": {
        "status": "ok"
      },
      "last_wal_replay": {
        "status": "ok"
      },
      "config": {
        "size": 800000,
        "lazy_cache_ttl": 259200000,
        "l1_enabled": true,
        "l1_total_cap": 8192,
        "l1_shard_cap": 32,
        "enable_ecs": false,
        "dump_interval": 36000,
        "wal_sync_interval": 1
      }
    }
  ]
}
```

`GET /api/v1/cache/cache_all/entries?limit=2` 返回示例：

```json
{
  "tag": "cache_all",
  "total": 2,
  "offset": 0,
  "limit": 2,
  "items": [
    {
      "key": "example.com. IN A",
      "domain_set": "记忆直连",
      "stored_time": "2026-03-13T11:20:00+08:00",
      "msg_expire": "2026-03-13T11:25:00+08:00",
      "cache_expire": "2026-03-13T12:20:00+08:00",
      "dns_message": ";; opcode: QUERY, status: NOERROR ..."
    }
  ]
}
```

字段说明：

- `key`: 前端使用的稳定标识
- `name`: 展示名称
- `tag`: 对应缓存插件 tag
- `backend_size`: L2 后端缓存条目数
- `l1_size`: L1 热缓存条目数
- `updated_keys`: 自上次 dump 以来变更的 key 数
- `counters`: 查询、命中、lazy 更新等累计统计
- `last_dump / last_load / last_wal_replay`: 最近一次持久化相关操作状态
- `config`: 当前缓存实例的关键运行参数快照

`GET /api/v1/data/domain_stats` 返回示例：

```json
{
  "items": [
    {
      "key": "fakeip",
      "name": "FakeIP 域名",
      "tag": "my_fakeiplist",
      "memory_id": "fakeip",
      "kind": "fakeip",
      "total_entries": 26982,
      "dirty_entries": 1113,
      "promoted_entries": 26723,
      "published_rules": 26723,
      "total_observations": 127517,
      "dropped_observations": 0,
      "dropped_by_buffer": 0,
      "dropped_by_cap": 0
    }
  ]
}
```

字段说明：

- `key`: 前端使用的稳定标识
- `name`: 展示名称
- `tag`: 对应 domain_output 插件 tag
- `memory_id`: 插件内部内存实例标识
- `kind`: 统计类型，例如 `fakeip`、`realip`、`nov4`、`nov6`、`generic`
- `total_entries`: 当前内存中统计条目数
- `dirty_entries`: 当前脏条目数
- `promoted_entries`: 已达到发布条件的条目数
- `published_rules`: 当前已发布规则数
- `total_observations`: 总观察次数
- `dropped_observations / dropped_by_buffer / dropped_by_cap`: 丢弃统计
- `cache_expires_at`
- `message`

### 2.8 系统操作

#### `POST /api/v1/system/restart`

稳定级别：`stable`

计划自重启。

请求体：

```json
{
  "delay_ms": 300
}
```

返回示例：

```json
{
  "status": "scheduled",
  "delay_ms": 300
}
```

### 2.9 上游配置

根路径：`/api/v1/upstream`

| 方法 | 子路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/tags` | `stable` | 获取扫描到的上游插件 tag |
| `GET` | `/config` | `stable` | 获取上游覆盖配置 |
| `PUT` | `/config` | `stable` | 全量保存上游配置（支持 `apply`） |
| `POST` | `/apply` | `stable` | 基于已保存配置触发运行时生效 |
| `GET` | `/items` | `stable` | 按 `plugin_tag` 查询上游列表 |
| `POST` | `/items` | `stable` | 新增单个上游 |
| `PUT` | `/items/{upstreamTag}` | `stable` | 更新单个上游 |
| `DELETE` | `/items/{upstreamTag}` | `stable` | 删除单个上游 |
| `POST` | `/config` | `compat` | 兼容接口：按插件 tag 覆盖保存并可立即生效 |

#### `PUT /api/v1/upstream/config`（推荐）

```json
{
  "config": {
    "aliapi_main": [
      {
        "tag": "up1",
        "enabled": true,
        "protocol": "doh",
        "addr": "https://dns.example/dns-query"
      }
    ]
  },
  "apply": true
}
```

- `apply: true`：保存后立即触发 runtime reload。
- `apply: false`：只落盘，不立即生效。

#### `POST /api/v1/upstream/apply`

请求体可选：

```json
{
  "plugin_tag": "aliapi_main"
}
```

- 不传 `plugin_tag`：全量生效
- 传 `plugin_tag`：仅生效指定插件 tag

#### `GET /api/v1/upstream/items?plugin_tag=aliapi_main`

返回指定插件组的上游数组。

#### `POST /api/v1/upstream/items`

```json
{
  "plugin_tag": "aliapi_main",
  "upstream": {
    "tag": "up2",
    "enabled": true,
    "protocol": "udp",
    "addr": "223.5.5.5"
  },
  "apply": false
}
```

#### `PUT /api/v1/upstream/items/{upstreamTag}`

```json
{
  "plugin_tag": "aliapi_main",
  "upstream": {
    "tag": "up2",
    "enabled": false,
    "protocol": "udp",
    "addr": "223.5.5.5"
  },
  "apply": true
}
```

#### `DELETE /api/v1/upstream/items/{upstreamTag}?plugin_tag=aliapi_main&apply=true`

- 删除后是否立即生效由 `apply` 查询参数控制。

#### 兼容接口：`POST /api/v1/upstream/config`

旧请求体仍可用：

```json
{
  "plugin_tag": "aliapi_main",
  "upstreams": [
    {
      "tag": "up1",
      "enabled": true,
      "protocol": "doh",
      "addr": "https://dns.example/dns-query"
    }
  ]
}
```

## 3. 开关 API

当前项目已经改为具名开关，不再建议使用旧的 `switch1/switch2/...` 语义。

### 3.1 核心开关接口（`stable`）

根路径：`/api/v1/control/switches`

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/control/switches` | `stable` | 获取全部开关状态 |
| `GET` | `/api/v1/control/switches/{name}` | `stable` | 获取单个开关状态 |
| `PUT` | `/api/v1/control/switches/{name}` | `stable` | 更新单个开关 |

返回示例：

```json
[
  { "name": "core_mode", "value": "secure" },
  { "name": "client_proxy_mode", "value": "all" }
]
```

更新请求体：

```json
{
  "value": "secure"
}
```

当前具名开关包括：

- `core_mode`
- `client_proxy_mode`
- `block_response`
- `block_query_type`
- `block_ipv6`
- `branch_cache`
- `main_cache`
- `ad_block`
- `cn_answer_mode`
- `udp_fast_path`
- `prefer_ipv4`
- `prefer_ipv6`

示例：

```json
[
  { "name": "core_mode", "value": "secure" },
  { "name": "client_proxy_mode", "value": "all" }
]
```

更新请求体：

```json
{
  "value": "secure"
}
```

说明：

- 旧的 `/plugins/switches/*` 聚合接口已移除
- 旧的 `/plugins/{switch_name}` 单实例接口已移除
- 开关能力现在只保留 `/api/v1/control/switches/*`

## 4. 常用插件 API

下面这些是当前前端和运维会实际调用的插件接口。

统一说明：

- 这一章默认不属于长期 `stable` 主接口层
- 除非特别说明，大多数条目应视为 `compat` 或 `internal`
- 如果后续已有或新增同能力的 `/api/*` 收口接口，应优先迁移到核心 API

### 4.1 Requery / 批量重建分流任务（`stable`）

根路径：`/api/v1/control/requery`

这组接口用于“批量重建分流 / 快速预热缓存”流程本身，不负责直接读写具体分流列表内容。

当前推荐优先使用聚合接口，减少前端请求次数：

- `GET /api/v1/control/requery/summary`
- `POST /api/v1/control/requery/rules/save`
- `POST /api/v1/control/requery/rules/flush`

典型调用链：

1. `GET /api/v1/control/requery/summary` 一次性获取配置、运行状态、队列预览、分流记忆库统计
2. `POST /api/v1/control/requery/trigger` 触发 `full_rebuild / quick_rebuild / quick_prewarm`
3. `POST /api/v1/control/requery/enqueue` 入队单域名按需刷新
4. `POST /api/v1/control/requery/cancel` 取消当前任务
5. `POST /api/v1/control/requery/scheduler/config` 更新定时刷新配置
6. `POST /api/v1/control/requery/rules/save` 批量保存当前分流规则
7. `POST /api/v1/control/requery/rules/flush` 批量清空动态分流规则
8. `GET /api/v1/control/requery/stats/source_file_counts` 获取刷新源文件统计

三种任务模式：

- `full_rebuild`
  - 优先处理运行时 `dirty / stale / 热点 / 验证到期` 候选，再补源文件长尾
  - 默认分两阶段执行：高优先级阶段 + 长尾补全阶段
  - 使用 `refresh_resolver_address` 和可选 `refresh_resolver_pool`
  - 适合完整重算分流结果
- `quick_rebuild`
  - 优先只取运行时 `dirty / stale / 热点 / 验证到期` 候选
  - 使用 `refresh_resolver_address` 和可选 `refresh_resolver_pool`
  - 适合日常快速收敛热点分流
- `quick_prewarm`
  - 优先只取运行时热点域名子集
  - 使用 `resolver_address`
  - 只预热缓存，不保存/清空/重写分流规则

其中 `full_rebuild` / `quick_rebuild` 会按配置执行：

1. 调用 `url_actions.save_rules` 先保存当前分流规则
2. 汇总运行时候选域名与 `domain_processing.source_files`
3. 按 `workflow.flush_mode` 决定是否先清空旧规则
4. 执行 DNS 重新查询
5. 再把新结果发布到分流记忆库

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/summary` | `stable` | 获取刷新分流聚合摘要 |
| `GET` | `/` | `stable` | 获取完整 requery 配置 |
| `GET` | `/status` | `stable` | 获取运行状态 |
| `POST` | `/trigger` | `stable` | 手动触发一次刷新任务 |
| `POST` | `/enqueue` | `stable` | 入队单域名刷新任务 |
| `POST` | `/cancel` | `stable` | 取消当前任务 |
| `POST` | `/scheduler/config` | `stable` | 更新调度配置 |
| `POST` | `/rules/save` | `stable` | 批量保存 `url_actions.save_rules` 中的目标 |
| `POST` | `/rules/flush` | `stable` | 批量清空 `url_actions.flush_rules` 中的目标 |
| `GET` | `/stats/source_file_counts` | `stable` | 获取各源文件条目统计 |

`GET /api/v1/control/requery/summary` 返回核心字段：

- `config.domain_processing.source_files`
- `config.url_actions.save_rules`
- `config.url_actions.flush_rules`
- `config.workflow.flush_mode`
- `config.workflow.mode`
- `config.scheduler.enabled`
- `config.scheduler.interval_minutes`
- `config.recovery.auto_resume`
- `config.recovery.checkpoint_batch_size`
- `config.recovery.resume_delay_ms`
- `config.execution_settings.queries_per_second`
- `config.execution_settings.quick_queries_per_second`
- `config.execution_settings.prewarm_queries_per_second`
- `config.execution_settings.quick_rebuild_limit`
- `config.execution_settings.prewarm_limit`
- `config.execution_settings.full_rebuild_priority_limit`
- `config.execution_settings.refresh_resolver_pool`
- `config.execution_settings.url_call_concurrency`
- `config.execution_settings.on_demand_batch_size`
- `status.task_state`
- `status.task_mode`
- `status.task_stage`
- `status.task_stage_label`
- `status.task_stage_processed`
- `status.task_stage_total`
- `status.last_run_mode`
- `status.progress`
- `status.pending_queue`
- `status.max_queue_size`
- `status.queue_preview`
- `status.last_error`
- `memory_stats`

`GET /api/v1/control/requery` 返回核心字段：

- `domain_processing.source_files`
- `url_actions.save_rules`
- `url_actions.flush_rules`
- `workflow.flush_mode`
- `workflow.mode`
- `scheduler.enabled`
- `scheduler.start_datetime`
- `scheduler.interval_minutes`
- `execution_settings.queries_per_second`
- `execution_settings.quick_queries_per_second`
- `execution_settings.prewarm_queries_per_second`
- `execution_settings.resolver_address`
- `execution_settings.refresh_resolver_address`
- `execution_settings.refresh_resolver_pool`
- `execution_settings.query_mode`
- `execution_settings.date_range_days`
  - 仅影响完整重建第二阶段扫描源文件时的长尾补全范围
- `execution_settings.quick_rebuild_limit`
- `execution_settings.prewarm_limit`
- `execution_settings.full_rebuild_priority_limit`
- `execution_settings.url_call_concurrency`
- `execution_settings.on_demand_batch_size`
- `status.task_state`
- `status.task_mode`
- `status.last_run_mode`
- `status.progress`
- `status.pending_queue`
- `full_rebuild_task`

说明：

- 旧的 `/plugins/requery/*` 接口已移除
- `requery` 现在只保留 `/api/v1/control/requery/*`

`POST /api/v1/control/requery/trigger` 请求体示例：

```json
{
  "mode": "quick_prewarm",
  "limit": 1000
}
```

`mode` 支持：

- `full_rebuild`
- `quick_rebuild`
- `quick_prewarm`

`POST /api/v1/control/requery/trigger` 成功返回示例：

```json
{
  "status": "success",
  "message": "快速预热任务已开始。",
  "task_mode": "quick_prewarm"
}
```

`POST /api/v1/control/requery/enqueue` 请求体示例：

```json
{
  "domain": "example.com",
  "memory_id": "my_fakeiplist",
  "qtype_mask": 1,
  "reason": "observed",
  "verify_url": "http://127.0.0.1:9099/api/v1/memory/my_fakeiplist/verify",
  "observed_at": "2026-03-12T10:00:00Z"
}
```

说明：

- `domain` 是必填项
- `memory_id` 用于标记回写目标分流记忆库
- `reason` 会影响队列优先级，常见值：`observed`、`stale`、`conflict`、`error`
- 如果队列判定为重复或无需处理，接口会返回 `202 Accepted` 且 `status=skipped`

`POST /api/v1/control/requery/enqueue` 成功返回示例：

```json
{
  "status": "queued",
  "domain": "example.com",
  "pending_queue": 3
}
```

`POST /api/v1/control/requery/rules/save` / `POST /api/v1/control/requery/rules/flush` 返回示例：

```json
{
  "action": "save_rules",
  "total": 8,
  "success": 8,
  "failed": 0,
  "duration_ms": 112,
  "items": [
    {
      "url": "http://127.0.0.1:9099/api/v1/memory/my_fakeiplist/save",
      "tag": "my_fakeiplist",
      "ok": true,
      "status_code": 200,
      "duration_ms": 9
    }
  ]
}
```

`POST /api/v1/control/requery/scheduler/config` 示例：

```json
{
  "enabled": true,
  "start_datetime": "2026-03-11T03:00:00Z",
  "interval_minutes": 1440,
  "date_range_days": 30,
  "mode": "hybrid",
  "queries_per_second": 100,
  "quick_queries_per_second": 300,
  "prewarm_queries_per_second": 500,
  "refresh_resolver_address": "127.0.0.1:7767",
  "refresh_resolver_pool": [
    "127.0.0.1:7767",
    "127.0.0.1:7768"
  ],
  "quick_rebuild_limit": 2000,
  "prewarm_limit": 1000,
  "full_rebuild_priority_limit": 4000
}
```

说明：

- `refresh_resolver_pool` 用于给 `quick_rebuild / full_rebuild` 提供多个刷新解析地址，任务执行时会轮询分配给 worker。
- `full_rebuild_priority_limit` 用于限制完整重建第一阶段的高优先级候选规模；超出的候选与源文件长尾一起进入第二阶段。
- 运行时高优先级候选来自分流记忆库的内存状态，而不是单纯全量扫描文本源文件。
- `recovery` 仅用于 `full_rebuild` 的断点恢复。
- `recovery.auto_resume` 默认为 `true`，重启后会自动尝试恢复中断的完整重建任务。
- `recovery.checkpoint_batch_size` 默认为 `256`，表示每处理多少个域名落一次 checkpoint。
- `recovery.resume_delay_ms` 默认为 `1500`，表示服务启动后延迟多久自动恢复任务。
- `full_rebuild_task` 是内部持久化的完整重建快照，包含剩余高优先级/长尾候选、阶段、已完成数量等信息。

优化说明与推荐配置：

- 相对旧版本，当前实现的主要优化点：
  - `quick_prewarm / quick_rebuild` 优先使用运行时 `dirty / stale / hot / refresh_due` 候选，不再默认先扫完整源文件
  - `full_rebuild` 改为两阶段：先跑高优先级候选，再补源文件长尾
  - `full_rebuild` 支持 checkpoint 和重启自动恢复，不再因为服务重启直接丢整轮任务
  - `save / flush / verify` 优先 direct-call 插件能力，不再优先走本机 HTTP 自调用
  - `refresh_resolver_pool` 支持多刷新解析地址轮询，降低单点瓶颈
  - `summary` 聚合接口收口前端状态请求，减少页面多路轮询
- 字段推荐理解：
  - `queries_per_second`：完整重建的基础 QPS，上限不要盲目拉高
  - `quick_queries_per_second`：快速重建的 QPS，适合高于完整重建
  - `prewarm_queries_per_second`：快速预热的 QPS，通常可以最高
  - `quick_rebuild_limit`：快速重建最多处理多少个高价值候选
  - `prewarm_limit`：快速预热最多预热多少个热点域名
  - `full_rebuild_priority_limit`：完整重建第一阶段最多先处理多少个高优先级候选
  - `date_range_days`：仅影响完整重建第二阶段源文件长尾补全的扫描范围
- 推荐配置思路：
  - 日常巡检：使用 `quick_rebuild`
  - 用户切换模式或更新后快速回暖：使用 `quick_prewarm`
  - 全量修正：只在低峰期跑 `full_rebuild`
  - 如果存在多个可用刷新解析地址，优先配置 `refresh_resolver_pool`
- 推荐起点：
  - `queries_per_second`: `80 ~ 150`
  - `quick_queries_per_second`: `200 ~ 400`
  - `prewarm_queries_per_second`: `300 ~ 600`
  - `quick_rebuild_limit`: `1000 ~ 3000`
  - `prewarm_limit`: `500 ~ 1500`
  - `full_rebuild_priority_limit`: `2000 ~ 6000`
- 资源影响说明：
  - `quick_prewarm` 对正常使用影响最小
  - `quick_rebuild` 影响通常可控
  - `full_rebuild` 仍然属于后台重查任务，建议安排在低峰时段
- 恢复语义说明：
  - 只对 `full_rebuild` 做持久化恢复
  - 服务重启后会从最近一个 checkpoint 继续
  - 如果在一个 checkpoint 批次中途重启，最多重跑该批次，不会从整轮任务起点重新开始
- 注意：
  - 这套任务会真实发 DNS 查询重建分流结果，不是单纯内存刷新
  - 即使做了增量优化，`full_rebuild` 也不应被当成“秒级无感操作”

`GET /api/v1/control/requery/stats/source_file_counts` 返回示例：

```json
{
  "status": "success",
  "data": [
    {
      "alias": "top_domains",
      "count": 128
    }
  ]
}
```

### 4.2 缓存 API（`stable`）

根路径：`/api/v1/cache`

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/stats` | `stable` | 查看所有缓存实例聚合统计 |
| `GET` | `/{tag}/stats` | `stable` | 查看单缓存实例统计 |
| `GET` | `/{tag}/entries` | `stable` | 按文本查看缓存内容 |
| `POST` | `/{tag}/flush` | `stable` | 清空缓存并触发后台持久化 |
| `POST` | `/{tag}/purge_domain` | `stable` | 按域名精确删除缓存项并立即 checkpoint |

`/{tag}/entries` 支持：`q, limit, offset`

`POST /api/v1/cache/{tag}/purge_domain` 请求体示例：

```json
{
  "qname": "example.com",
  "qtype": 1
}
```

说明：

- `qname` 必填，会自动规范化为 FQDN。
- `qtype` 选填。
- 省略 `qtype` 时，会删除该域名在当前缓存实例中的所有记录。
- 该接口会同步清理内存缓存并立即执行 checkpoint，避免重启后旧 WAL 把条目放回来。
- 旧插件缓存路由 `/plugins/{cache_tag}/*` 已移除。

补充：

- 命中 `DDNS域名` 标签的缓存项，过期后不会再走 lazy stale 返回旧值，而是直接回源重查。

### 4.3 domain_output / 分流记忆库与域名输出（`stable`）

根路径：`/api/v1/memory/{memory_tag}`

这组接口是“刷新分流”真正读写的数据面。`requery` 任务最终会把结果发布到这些记忆库。

常见 tag：

- `top_domains`
- `my_fakeiplist`
- `my_realiplist`
- `my_nov4list`
- `my_nov6list`
- `my_nodenov4list`
- `my_nodenov6list`
- `my_notinlist`

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `POST` | `/flush` | `stable` | 清空并写盘 |
| `POST` | `/save` | `stable` | 保存当前内存到文件 |
| `GET` | `/entries` | `stable` | 查看记忆数据 |
| `GET` | `/stats` | `stable` | 获取统计信息 |
| `POST` | `/verify` | `stable` | 标记域名已验证、清理 dirty 状态 |

`POST /verify` 示例：

```json
{
  "domain": "example.com",
  "verified_at": "2026-03-11T12:00:00Z"
}
```

`GET /api/v1/memory/{memory_tag}/stats` 返回核心字段：

- `memory_id`
- `kind`
- `total_entries`
- `dirty_entries`
- `promoted_entries`
- `published_rules`
- `total_observations`
- `dropped_observations`
- `dropped_by_buffer`
- `dropped_by_cap`

`GET /api/v1/memory/{memory_tag}/entries?limit=2` 返回示例：

```json
{
  "tag": "my_fakeiplist",
  "total": 27423,
  "offset": 0,
  "limit": 2,
  "items": [
    {
      "domain": "www.jetbrains.com",
      "count": 3226,
      "date": "2026-03-12",
      "qtype_mask": 1,
      "score": 3226,
      "promoted": true
    }
  ]
}
```

`POST /verify` 用途：

- 把指定域名从 `dirty` 标记回写为 `clean`
- 记录 `verified_at`
- 会立即保存当前记忆库内容
- 旧的 `/plugins/{memory_tag}/show|save|flush|verify|restartall` 已移除

### 4.4 可编辑规则列表：IPSet / DomainSet Light / Rewrite（`stable`）

这类接口常见于：

- `client_ip`
- `direct_ip`
- `whitelist`
- `blocklist`
- `greylist`
- `realiplist`
- `cnfakeipfilter`
- `rewrite`

常见路径：

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/lists/{tag}` | `stable` | 查看当前列表内容 |
| `PUT` | `/api/v1/lists/{tag}` | `stable` | 用 `values[]` 替换内容 |

`PUT /api/v1/lists/{tag}` 请求体：

```json
{
  "values": [
    "full:example.com",
    "1.2.3.4/32"
  ]
}
```

说明：

- 旧的 `/plugins/{tag}/show` 和 `/plugins/{tag}/post` 在上述可编辑列表标签上已移除。
- `save/flush` 这类历史动作不再由列表管理页直接使用。
- `IPSet` 返回和接收的是 IP / CIDR
- `DomainSet` 返回和接收的是域名规则文本
- `GET /api/v1/lists/{tag}?limit=2` 返回结构化 JSON，不再返回纯文本

`GET /api/v1/lists/{tag}?limit=2` 返回示例：

```json
{
  "tag": "whitelist",
  "total": 2,
  "offset": 0,
  "limit": 2,
  "items": [
    { "value": "full:localhost.pan.baidu.com" },
    { "value": "domain:alibabachengdun.com" }
  ]
}
```

### 4.5 在线分流规则源（`stable`）

适用于：

- `geosite_cn`
- `geosite_no_cn`
- `geoip_cn`
- `cuscn`
- `cusnocn`

根路径：`/api/v1/rules/diversion`

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/rules/diversion` | `stable` | 聚合获取所有在线分流规则源 |
| `PUT` | `/api/v1/rules/diversion/{type}/{name}` | `stable` | 新增或更新指定类型规则源 |
| `DELETE` | `/api/v1/rules/diversion/{type}/{name}` | `stable` | 删除指定类型规则源 |
| `POST` | `/api/v1/rules/diversion/{type}/{name}/update` | `stable` | 触发指定规则源后台更新 |

说明：

- 旧的 `/plugins/{tag}/config*` 和 `/plugins/{tag}/update/{name}` 已移除
- 前端不再需要知道 `geosite_cn`、`geoip_cn` 这类插件 tag

### 4.6 AdGuard 在线广告规则（`stable`）

根路径：`/api/v1/rules/adguard`

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/rules/adguard` | `stable` | 获取规则列表 |
| `POST` | `/api/v1/rules/adguard` | `stable` | 新增规则 |
| `PUT` | `/api/v1/rules/adguard/{id}` | `stable` | 更新规则 |
| `DELETE` | `/api/v1/rules/adguard/{id}` | `stable` | 删除规则 |
| `POST` | `/api/v1/rules/adguard/update` | `stable` | 更新所有启用规则 |

新增 / 更新请求体核心字段：

```json
{
  "name": "AdGuard Base",
  "url": "https://example.com/rules.txt",
  "enabled": true,
  "auto_update": true,
  "update_interval_hours": 24
}
```

### 4.7 客户端别名与反查工具（`stable`）

| 方法 | 路径 | 等级 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/control/clientname` | `stable` | 获取客户端别名映射 |
| `PUT` | `/api/v1/control/clientname` | `stable` | 更新客户端别名映射 |
| `GET` | `/api/v1/reverse_lookup?ip=...` | `stable` | 反查 IP 对应域名 |

`PUT /api/v1/control/clientname` 请求体示例：

```json
{
  "192.168.1.2": "MacBook",
  "192.168.1.3": "Apple TV"
}
```

`GET /api/v1/reverse_lookup?ip=1.1.1.1` 返回示例：

```json
{
  "ip": "1.1.1.1",
  "domain": "one.one.one.one."
}
```

说明：

- 旧的 `/plugins/clientname` 已移除
- 旧的 `/plugins/reverse_lookup` 已移除

## 5. 当前前端重点依赖的接口

这一节描述的是“当前实现现状”，不是稳定性推荐顺序。

也就是说：

- 这里列出的 `/plugins/*` 路径不一定是长期推荐接口
- 它们只是当前前端实际仍在调用
- 后续若存在同能力的核心 API，应优先迁移

如果你是在维护 `log.html` / `log.js` 这套前端，优先关注这些接口：

### 5.1 页面状态与统计

- `GET /api/v1/audit/status`
- `GET /api/v1/audit/capacity`
- `GET /api/v2/audit/stats`
- `GET /api/v2/audit/rank/domain`
- `GET /api/v2/audit/rank/client`
- `GET /api/v2/audit/rank/domain_set`
- `GET /api/v2/audit/rank/slowest`
- `GET /api/v2/audit/logs`
- `GET /metrics`

其中 `GET /api/v1/audit/capacity` 现在返回的是“审计存储设置 + 当前磁盘占用”，不再只是单一条目容量。

### 5.2 系统控制

- `GET /api/v1/control/switches`
- `PUT /api/v1/control/switches/{name}`
- `GET /api/v1/update/status`
- `POST /api/v1/update/check`
- `POST /api/v1/update/apply`
- `GET /api/v1/overrides`
- `POST /api/v1/overrides`
- `GET /api/v1/upstream/tags`
- `GET /api/v1/upstream/config`
- `PUT /api/v1/upstream/config`
- `POST /api/v1/upstream/apply`
- `GET /api/v1/upstream/items?plugin_tag=...`
- `POST /api/v1/upstream/items`
- `PUT /api/v1/upstream/items/{upstreamTag}`
- `DELETE /api/v1/upstream/items/{upstreamTag}?plugin_tag=...`
- `POST /api/v1/upstream/config`（兼容）
- `POST /api/v1/system/restart`

### 5.3 刷新与缓存

批量重建 / 预热主流程：

- `GET /api/v1/control/requery/summary`
- `GET /api/v1/control/requery`
- `GET /api/v1/control/requery/status`
- `POST /api/v1/control/requery/trigger`
- `POST /api/v1/control/requery/enqueue`
- `POST /api/v1/control/requery/cancel`
- `POST /api/v1/control/requery/scheduler/config`
- `POST /api/v1/control/requery/rules/save`
- `POST /api/v1/control/requery/rules/flush`
- `GET /api/v1/control/requery/stats/source_file_counts`

分流记忆库保存 / 清空 / 校验：

- `GET /api/v1/memory/{memory_tag}/stats`
- `GET /api/v1/memory/{memory_tag}/entries`
- `POST /api/v1/memory/{memory_tag}/save`
- `POST /api/v1/memory/{memory_tag}/flush`
- `POST /api/v1/memory/{memory_tag}/verify`

当前前端内置批量保存 / 批量清空会覆盖这些常见 tag：

- `top_domains`
- `my_fakeiplist`
- `my_realiplist`
- `my_nov4list`
- `my_nov6list`
- `my_nodenov4list`
- `my_nodenov6list`
- `my_notinlist`

缓存清理：

- `POST /api/v1/cache/cache_all/flush`
- `POST /api/v1/cache/cache_all_noleak/flush`

### 5.4 规则与列表管理

- `GET /api/v1/rules/adguard`
- `POST /api/v1/rules/adguard`
- `PUT /api/v1/rules/adguard/{id}`
- `DELETE /api/v1/rules/adguard/{id}`
- `POST /api/v1/rules/adguard/update`
- `GET /api/v1/rules/diversion`
- `PUT /api/v1/rules/diversion/{type}/{name}`
- `DELETE /api/v1/rules/diversion/{type}/{name}`
- `POST /api/v1/rules/diversion/{type}/{name}/update`
- `GET /api/v1/lists/{tag}`
- `PUT /api/v1/lists/{tag}`
- `GET /api/v1/control/clientname`
- `PUT /api/v1/control/clientname`
- `domain_output` 记忆库已统一到 `/api/v1/memory/{tag}/*`

## 6. 迁移说明

### 6.1 旧编号开关接口

文档和前端都应当只使用具名开关核心接口：

- `GET /api/v1/control/switches`
- `GET /api/v1/control/switches/{name}`
- `PUT /api/v1/control/switches/{name}`

### 6.2 插件 tag 与插件类型不是一回事

例如：

- `type: cache`
- `tag: cache_all`

当前核心缓存路径是：

- `/api/v1/cache/cache_all/entries`
- `/api/v1/cache/cache_all/stats`

不是 `/api/v1/cache/cache/show`。

### 6.3 稳定性使用建议

建议按以下顺序选择接口：

1. 优先使用 `2.x` 中标为 `stable` 的核心 API
2. `3.x / 4.x` 中标为 `compat` 的接口，只在当前没有核心替代时使用
3. `internal` 接口默认只用于：
   - 调试
   - 插件运维
   - 兼容迁移
