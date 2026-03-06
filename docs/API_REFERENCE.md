# mosdns API 接口文档（完整）

> 说明：本文档以源码路由注册与处理逻辑为准，覆盖核心 API 与插件 API。插件接口通过 `/plugins/{tag}` 暴露，`{tag}` 为配置文件中插件实例 `tag`。

## 1. 基础约定

- 默认 API 根（示例）：`http://127.0.0.1:9099`
- CORS：全局允许 `GET, POST, OPTIONS, PUT, DELETE`
- 统一响应并不完全一致（部分接口返回纯文本，部分返回 JSON）
- 未匹配路径会返回包含可用路由列表的文本说明

## 2. 核心 API

## 2.1 系统与观测类

| 方法 | 路径 | 说明 | 请求体 | 返回 |
|---|---|---|---|---|
| GET | `/metrics` | Prometheus 指标 | 无 | 文本指标 |
| GET | `/debug/pprof/*` | pprof 入口 | 无 | pprof 页面/数据 |
| GET | `/debug/pprof/cmdline` | pprof cmdline | 无 | 文本 |
| GET | `/debug/pprof/profile` | pprof profile | 无 | 二进制 |
| GET | `/debug/pprof/symbol` | pprof symbol | 无 | 文本 |
| GET | `/debug/pprof/trace` | pprof trace | 无 | 二进制 |

## 2.2 日志抓取 API（v1）

### `POST /api/v1/capture/start`

- 作用：开始内存日志抓取并临时提升日志级别到 `DEBUG`
- Body（可选）：

```json
{
  "duration_seconds": 120
}
```

- 约束：`duration_seconds` 取值 `1~600`，默认 `120`
- 成功返回：纯文本
- 失败：`400`（JSON 解析错误或参数非法）

### `GET /api/v1/capture/logs`

- 作用：获取抓取日志（读后清空缓冲）
- 返回：JSON 数组（结构化日志对象）

## 2.3 审计 API（v1）

根路径：`/api/v1/audit`

| 方法 | 子路径 | 说明 | 请求体 |
|---|---|---|---|
| POST | `/start` | 开始审计采集 | 无 |
| POST | `/stop` | 停止审计采集 | 无 |
| GET | `/status` | 采集状态 | 无 |
| GET | `/logs` | 审计日志列表 | 无 |
| POST | `/clear` | 清空审计日志 | 无 |
| GET | `/capacity` | 获取容量 | 无 |
| POST | `/capacity` | 设置容量并清空现有日志 | `{"capacity": <int>}` |

常见响应示例：

```json
{
  "capturing": true
}
```

```json
{
  "capacity": 100000
}
```

## 2.4 审计 API（v2）

根路径：`/api/v2/audit`

| 方法 | 子路径 | 说明 | 查询参数 |
|---|---|---|---|
| GET | `/stats` | 总请求数与平均耗时 | 无 |
| GET | `/rank/domain` | 域名排行 | `limit`（默认20） |
| GET | `/rank/client` | 客户端排行 | `limit`（默认20） |
| GET | `/rank/domain_set` | 规则集排行 | `limit`（默认20） |
| GET | `/rank/slowest` | 慢查询排行 | `limit`（默认100） |
| GET | `/logs` | 分页 + 过滤日志 | `page,limit,domain,answer_ip,cname,client_ip,q,exact` |

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

审计日志字段（核心）：

- `client_ip`
- `query_type`, `query_name`, `query_class`
- `query_time`, `duration_ms`
- `trace_id`
- `response_code`, `response_flags`（`aa/tc/ra`）
- `answers[]`（`type/ttl/data`）
- `domain_set`

## 2.5 全局覆盖配置 API

根路径：`/api/v1/overrides`

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/` | 读取覆盖配置与替换命中统计 |
| POST | `/` | 保存覆盖配置 |

`POST /api/v1/overrides/` 请求体示例：

```json
{
  "socks5": "127.0.0.1:1080",
  "ecs": "1.2.3.4",
  "replacements": [
    {
      "original": "old-value",
      "new": "new-value",
      "comment": "replace example"
    }
  ]
}
```

## 2.6 配置管理 API

### `POST /api/v1/config/export`

- 作用：打包目录为 ZIP 下载
- 请求体：

```json
{
  "dir": "/path/to/mosdns"
}
```

- 返回：`application/zip`

### `POST /api/v1/config/update_from_url`

- 作用：下载 ZIP -> 本地备份 -> 覆盖 -> 触发重启
- 请求体：

```json
{
  "url": "https://example.com/config.zip",
  "dir": "/path/to/mosdns"
}
```

- 返回：JSON（`status: success`）或错误文本

## 2.7 在线更新 API

根路径：`/api/v1/update`

| 方法 | 子路径 | 说明 |
|---|---|---|
| GET | `/status` | 查询更新状态（带缓存） |
| POST | `/check` | 强制刷新更新状态 |
| POST | `/apply` | 执行更新 |

`POST /apply` 请求体：

```json
{
  "force": false,
  "prefer_v3": false
}
```

更新状态结构（`UpdateStatus` 关键字段）：

- `current_version`, `latest_version`
- `release_url`, `architecture`
- `asset_name`, `download_url`
- `asset_signature`, `current_signature`
- `checked_at`, `cache_expires_at`
- `update_available`, `cached`
- `pending_restart`
- `amd64_v3_capable`, `current_is_v3`

## 2.8 系统操作 API

### `POST /api/v1/system/restart`

- 作用：计划自重启（非 Windows）
- 请求体：

```json
{
  "delay_ms": 300
}
```

- Windows 返回 `501`：`self-restart is not supported on Windows`

## 2.9 上游配置 API

根路径：`/api/v1/upstream`

| 方法 | 子路径 | 说明 |
|---|---|---|
| GET | `/tags` | 获取扫描到的上游相关插件标签（如 aliapi） |
| GET | `/config` | 获取当前上游覆盖配置 |
| POST | `/config` | 保存指定插件标签的上游列表 |

`POST /api/v1/upstream/config` 请求体：

```json
{
  "plugin_tag": "aliapi_main",
  "upstreams": [
    {
      "tag": "up1",
      "enabled": true,
      "protocol": "doh",
      "addr": "https://dns.example/dns-query",
      "socks5": "127.0.0.1:1080"
    }
  ]
}
```

`upstreams[]` 字段支持：

- 通用：`tag, enabled, protocol, addr, dial_addr, idle_timeout, upstream_query_timeout`
- DNS：`enable_pipeline, enable_http3, insecure_skip_verify, socks5, so_mark, bind_to_device, bootstrap, bootstrap_version`
- AliAPI：`account_id, access_key_id, access_key_secret, server_addr, ecs_client_ip, ecs_client_mask`

## 3. 插件 API（`/plugins/{tag}`）

## 3.1 路由映射说明

- 每个插件实例通过 `bp.RegAPI(mux)` 挂载。
- `tag` 来自配置中的插件实例标识，不是插件类型名。
- 同类型多个实例会对应多个不同路径前缀。

示例：

- 配置：`tag: cache_main`, `type: cache`
- 路径：`/plugins/cache_main/show`

## 3.2 已实现插件 API 的类型与接口

## A. 开关类

### `switch`（`plugin/switch/switch`）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/` | 获取当前值（纯文本） |
| PUT | `/` | 更新值（JSON/form/raw body） |
| POST | `/` | 同 PUT |

PUT/POST JSON 示例：

```json
{
  "value": "on"
}
```

### `switch1` ~ `switch16`

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/show` | 查看当前值 |
| POST | `/post` | 更新值（支持 JSON 或 form） |

## B. 规则集合类

### `domain_set` / `domain_set_light`

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/show` | 输出规则文本（`domain_set_light` 支持 `q/limit/offset`） |
| GET | `/save` | 保存内存规则到文件 |
| POST | `/post` | 以 JSON 覆盖规则 |

`POST /post` 请求体：

```json
{
  "values": ["full:example.com", "domain:google.com"]
}
```

### `ip_set`

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/show` | 输出前缀列表 |
| GET | `/save` | 保存到文件 |
| GET | `/flush` | 清空并保存 |
| POST | `/post` | 用 `values[]` 覆盖列表 |

请求体示例：

```json
{
  "values": ["1.1.1.1", "10.0.0.0/8"]
}
```

### `sd_set` / `sd_set_light` / `si_set` / `nft_add`

通用接口：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/config` | 查询规则源列表 |
| POST | `/update/{name}` | 手工触发某规则源更新（异步） |
| PUT | `/config/{name}` | 新增或更新规则源 |
| DELETE | `/config/{name}` | 删除规则源 |

`sd_set/sd_set_light` 的 `RuleSource`：

- `name, type, files, url, enabled, enable_regexp, auto_update, update_interval_hours, rule_count, last_updated`

`si_set/nft_add` 的 `RuleSource`：

- `name, type, files, url, enabled, auto_update, update_interval_hours, rule_count, last_updated`

## C. 执行器与状态类

### `rewrite`

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/show` | 查看 rewrite 规则 |
| POST | `/post` | 覆盖 rewrite 规则 |

请求体：

```json
{
  "values": ["full:a.com 1.1.1.1", "domain:b.com 2.2.2.2"]
}
```

### `cache`

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/flush` | 清空缓存并异步 dump |
| GET | `/dump` | 导出缓存 dump（二进制） |
| GET | `/save` | 保存缓存到 dump_file |
| POST | `/load_dump` | 导入 dump（二进制 body） |
| GET | `/show` | 文本分页查询（支持 `q/limit/offset`） |
| GET | `/stats` | 输出当前缓存实例的 JSON 统计信息（含 L1/L2、snapshot/WAL 状态） |

常用配置字段：

- `size`
- `lazy_cache_ttl`
- `enable_ecs`
- `exclude_ip`
- `dump_file`
- `dump_interval`
- `wal_file`：可选，启用 WAL 持久化
- `wal_sync_interval`：可选，WAL 刷盘间隔（秒）

运维说明：

- 生产启用建议同时保留 `dump_file` 和 `wal_file`：snapshot 负责 checkpoint，WAL 负责 checkpoint 间增量恢复
- 回滚到仅 snapshot 模式时，删除 `wal_file` / `wal_sync_interval` 即可，`dump_file` 兼容不变
- Web/UI 的缓存运行态面板应读取 `/plugins/{cache_tag}/stats`；系统级资源指标仍读取 `/metrics`

### `adguard_rule`

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/rules` | 规则源列表 |
| POST | `/rules` | 新增规则源 |
| PUT | `/rules/{id}` | 更新规则源 |
| DELETE | `/rules/{id}` | 删除规则源 |
| POST | `/update` | 手工更新所有启用规则 |

`OnlineRule` 请求/响应字段：

- `id, name, url, enabled, auto_update, update_interval_hours, rule_count, last_updated`

### `requery`

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/` | 获取完整配置 |
| GET | `/status` | 获取任务状态 |
| POST | `/trigger` | 启动任务 |
| POST | `/cancel` | 取消任务 |
| POST | `/scheduler/config` | 更新调度配置 |
| GET | `/stats/source_file_counts` | 统计源文件域名数量 |

`requery` 新增配置口径：

- `workflow.flush_mode`: `none`（推荐）或 `legacy`
- `workflow.save_before_refresh` / `workflow.save_after_refresh`
- `execution_settings.refresh_resolver_address`: 旁路刷新入口，推荐 `127.0.0.1:7767`
- `execution_settings.query_mode`: `observed`（推荐）、`dual`、`a`、`aaaa`

`POST /scheduler/config` 请求体：

```json
{
  "enabled": true,
  "start_datetime": "2026-03-04T00:00:00Z",
  "interval_minutes": 60,
  "date_range_days": 30
}
```

### `webinfo`

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/` | 读取任意 JSON 数据 |
| PUT | `/` | 覆盖写入任意 JSON 数据 |

### `reverse_lookup`

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/` | 查询反查缓存（`?ip=1.2.3.4`） |

### `domain_output`

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/flush` | 清空内存态并立即刷新输出 |
| GET | `/save` | 保存当前输出 |
| GET | `/show` | 按计数倒序显示（`q/limit/offset`） |
| GET | `/stats` | 查看晋升策略、已晋升条目和 dropped observations |
| GET | `/restartall` | 触发 mosdns 自重启 |

`domain_output` 新增 `policy` 配置块：

- `kind`: `generic/realip/fakeip/nov4/nov6`
- `promote_after`: 达到阈值后才进入规则文件和 `domain_set_url`
- `decay_days`: 超过天数后不再继续发布旧记忆
- `track_qtype`: 记录 `A/AAAA` 观察结果，用于 `nov4/nov6` 和 refresh 定向查询
- `publish_mode`: `all` 或 `promoted_only`

## 4. UI 页面接口（非 JSON API）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/` | 默认页面 `mosdnsp.html` |
| GET | `/graphic` | 图形页 `mosdns.html` |
| GET | `/log` | 日志页面 |
| GET | `/plog` | 纯文本日志页 |
| GET | `/rlog` | 重定向到 `/log` |
| GET | `/assets/*` | 静态资源 |

## 5. 错误处理与状态码约定

- 常见成功：`200, 201, 202, 204`
- 参数错误：`400`
- 资源不存在：`404`
- 业务冲突：`409`（如 requery 正在执行）
- 服务端错误：`500`
- 外部依赖失败：`502`（更新接口）
- 平台不支持：`501`（Windows 自重启）

## 6. 调试建议

- 先调用错误路径触发路由清单，快速确认实际可用接口。
- 插件接口务必先确认 `tag` 是否与配置一致。
- 对高频查询接口优先使用分页参数，避免一次拉全量数据。
