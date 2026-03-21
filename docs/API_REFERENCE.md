# mosdns API Reference

## 文档入口

- Swagger UI：运行服务后访问 `http://127.0.0.1:9099/ui/api-docs.html`
- OpenAPI：运行服务后访问 `http://127.0.0.1:9099/ui/openapi.yaml`
- 本文档：用于快速定位模块、调用方式、兼容别名和关键行为

本次文档只覆盖当前源码真实注册的接口：

- `/api/v1/*`
- `/api/v3/audit/*`

不再保留旧版 `/api/v2/*`、历史 `/plugins/*`、旧审计接口的说明。

## 通用约定

- 默认地址：`http://127.0.0.1:9099`
- 核心 JSON 错误格式：

```json
{
  "code": "INVALID_REQUEST_BODY",
  "error": "Invalid request body"
}
```

- requery 插件单独使用：

```json
{
  "status": "error",
  "message": "Failed to load runtime jobs"
}
```

- 审计时间参数 `from` / `to` 支持两种格式：
  - Unix 毫秒时间戳
  - RFC3339
- `GET /api/v1/update/status` 和 `POST /api/v1/update/check` 即使远程检查失败，当前实现仍返回 `200`，失败原因写在 `message`
- `POST /api/v1/config/export` 返回 `application/zip`
- `POST /api/v1/system/restart`、`POST /api/v1/update/apply`、`POST /api/v1/upstream/apply`、`POST /api/v1/capture/start` 都支持空请求体

## 1. Capture

| 方法 | 路径 | 说明 | 主要请求 | 主要返回 |
| --- | --- | --- | --- | --- |
| `POST` | `/api/v1/capture/start` | 开始临时抓取运行日志 | `{ "duration_seconds": 120 }`，默认 `120`，范围 `1~600` | `{ "message": "...", "duration_seconds": 120 }` |
| `GET` | `/api/v1/capture/logs` | 读取并清空本次抓取日志 | 无 | 结构化日志数组 |

## 2. Audit v3

| 方法 | 路径 | 说明 | 常用参数 / 请求体 | 主要返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v3/audit/overview` | 获取当前概览窗口统计 | `window` | `AuditOverview` |
| `GET` | `/api/v3/audit/timeseries` | 获取趋势图数据 | `from,to,step` | `AuditTimeseriesPoint[]` |
| `GET` | `/api/v3/audit/rank/domain` | 域名排行 | `from,to,limit` | `AuditRankItem[]` |
| `GET` | `/api/v3/audit/rank/client` | 客户端排行 | `from,to,limit` | `AuditRankItem[]` |
| `GET` | `/api/v3/audit/rank/domain_set` | 规则集排行 | `from,to,limit` | `AuditRankItem[]` |
| `GET` | `/api/v3/audit/logs` | 原始日志查询兼容入口 | `from,to,limit,cursor,q,exact,domain,client_ip,rcode,domain_set,cache_status,upstream_tag,transport,answer` | `AuditLogsResponse` |
| `POST` | `/api/v3/audit/logs/search` | 结构化原始日志搜索 | `AuditLogSearchRequest` | `AuditLogsResponse` |
| `GET` | `/api/v3/audit/logs/slow` | 慢查询列表 | `from,to,limit` | `AuditLog[]` |
| `GET` | `/api/v3/audit/settings` | 审计设置和当前存储状态 | 无 | `AuditSettingsResponse` |
| `PUT` | `/api/v3/audit/settings` | 更新审计设置，并写回 `config/config.yaml` 的 `audit:` 段 | `AuditSettingsUpdateRequest` | `AuditSettingsResponse` |
| `POST` | `/api/v3/audit/clear` | 清空原始日志和聚合数据 | 无 | `{ "message": "..." }` |

说明：

- `overview.total_query_count` / `overview.total_average_duration_ms` 表示保留期内累计统计
- `logs.next_cursor` 用于顺序翻页，不再走 `page` / `offset`
- 推荐使用 `POST /api/v3/audit/logs/search` 进行新搜索，对关键词、字段范围、时间范围和高级过滤做统一表达
- `GET /api/v3/audit/logs` 保留为兼容入口，主要用于简单 query string 查询
- `PUT /settings` 保存后立即生效，无需重启

### 2.1 `POST /api/v3/audit/logs/search`

推荐请求体结构：

```json
{
  "time_range": {
    "from": "2026-03-17T16:00:00+08:00",
    "to": "2026-03-18T16:00:00+08:00"
  },
  "page": {
    "limit": 50,
    "cursor": ""
  },
  "keyword": {
    "value": "dns.google",
    "mode": "fuzzy",
    "fields": ["server_name", "upstream_tag", "query_name"]
  },
  "filters": {
    "response_code": "NOERROR",
    "has_answer": true,
    "duration_ms_min": 1,
    "duration_ms_max": 100,
    "answer": {
      "value": "8.8.8.8",
      "mode": "exact"
    }
  }
}
```

字段说明：

- `time_range.from/to`：支持 RFC3339 或 Unix 毫秒时间戳；省略时默认最近 24 小时
- `page.limit/cursor`：顺序翻页参数
- `keyword.mode`：`fuzzy` 或 `exact`
- `keyword.fields`：可选，支持 `query_name`、`client_ip`、`trace_id`、`domain_set`、`answer`、`query_type`、`query_class`、`response_code`、`upstream_tag`、`transport`、`server_name`、`url_path`、`cache_status`
- `filters.*`：支持结构化过滤；文本过滤字段使用 `{ "value": "...", "mode": "exact|fuzzy" }`
- `filters.has_answer`：只看有应答或无应答
- `filters.duration_ms_min/max`：按处理耗时区间过滤

返回仍为 `AuditLogsResponse`：

- `summary.matched_count`：当前条件下命中的总条数
- `summary.average_duration_ms`：当前条件下平均耗时
- `summary.max_duration_ms`：当前条件下最慢耗时
- `logs[]`：当前页日志
- `next_cursor`：下一页游标，空字符串表示没有更多结果

## 3. Runtime

| 方法 | 路径 | 说明 | 主要请求 | 主要返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/control/summary` | SQLite 运行态摘要 | 无 | `RuntimeSummaryResponse` |
| `GET` | `/api/v1/control/health` | 运行态健康检查 | 无 | `RuntimeHealthResponse` |
| `GET` | `/api/v1/control/events` | 系统事件列表 | `component,limit` | `SystemEventEntry[]` |
| `GET` | `/api/v1/control/shunt/explain` | 分流决策解释 | `domain` 必填，`qtype` 可选，`live` 默认 `true` | `ShuntExplainResult` |
| `GET` | `/api/v1/control/shunt/conflicts` | 分流冲突分析 | `live` 默认 `true`，`limit` 可选 | `ShuntConflictsResponse` |

说明：

- `shunt/explain` 用于分析一个域名在当前规则和开关组合下的最终动作
- `shunt/conflicts` 返回冲突规则键、来源 provider 和规则输出标签

## 4. Overrides 与 Clientname

### 4.1 推荐入口

| 方法 | 路径 | 说明 | 请求体 | 返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/overrides` | 读取全局覆盖配置 | 无 | `GlobalOverridesResponse` |
| `POST` | `/api/v1/overrides` | 保存全局覆盖配置 | `GlobalOverrides` | `{ "message": "..." }` |
| `GET` | `/api/v1/control/clientname` | 读取客户端备注映射 | 无 | `map[string]string` |
| `PUT` | `/api/v1/control/clientname` | 替换客户端备注映射 | `map[string]string` | 同请求体 |

### 4.2 兼容别名

| 方法 | 路径 | 状态 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/control/overrides` | `compat` | 等价于 `/api/v1/overrides` |
| `POST` | `/api/v1/control/overrides` | `compat` | 等价于 `/api/v1/overrides` |

说明：

- `GlobalOverrides` 里是用户可编辑的全局代理/ECS/替换规则
- 返回体里的 `replacements[].result` 是运行态命中状态，不是持久化字段

## 5. Switches

| 方法 | 路径 | 说明 | 请求体 | 返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/control/switches` | 获取全部开关 | 无 | `SwitchState[]` |
| `GET` | `/api/v1/control/switches/{name}` | 获取单个开关 | 无 | `SwitchState` |
| `PUT` | `/api/v1/control/switches/{name}` | 修改单个开关 | 支持 JSON、表单、纯文本三种输入 | `SwitchState` |

当前支持的开关名：

- `block_response`
- `client_proxy_mode`
- `branch_cache`
- `block_query_type`
- `block_ipv6`
- `ad_block`
- `prefer_ipv4`
- `cn_answer_mode`
- `prefer_ipv6`
- `main_cache`
- `udp_fast_path`

值约束：

- `on/off`：`block_response`、`branch_cache`、`block_query_type`、`block_ipv6`、`ad_block`、`prefer_ipv4`、`prefer_ipv6`、`main_cache`、`udp_fast_path`
- `all/blacklist/whitelist`：`client_proxy_mode`
- `realip/fakeip`：`cn_answer_mode`

## 6. Requery

| 方法 | 路径 | 说明 | 主要请求 | 主要返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/control/requery` | 当前 requery 配置 | 无 | `RequeryConfig` |
| `GET` | `/api/v1/control/requery/status` | 当前运行状态快照 | 无 | `RequeryStatusSnapshot` |
| `GET` | `/api/v1/control/requery/summary` | 配置、状态、记忆池和最近运行历史聚合 | 无 | `RequerySummaryResponse` |
| `GET` | `/api/v1/control/requery/jobs` | 运行态任务定义 | 无 | `RequeryJob[]` |
| `GET` | `/api/v1/control/requery/runs` | 最近运行历史 | `limit` | `RequeryRun[]` |
| `GET` | `/api/v1/control/requery/checkpoints` | 断点快照 | `run_id,limit` | `RequeryCheckpoint[]` |
| `POST` | `/api/v1/control/requery/trigger` | 触发任务 | `{ "mode": "full_rebuild|quick_rebuild|quick_prewarm", "limit": 0 }` | `RequeryTriggerResponse` |
| `POST` | `/api/v1/control/requery/enqueue` | 加入按需刷新队列 | `RequeryRefreshJob` | `202` + `RequeryEnqueueResponse` |
| `POST` | `/api/v1/control/requery/cancel` | 取消当前任务 | 无 | `StatusMessageResponse` |
| `POST` | `/api/v1/control/requery/scheduler/config` | 更新调度与执行参数 | `RequerySchedulerUpdateRequest` | `StatusMessageResponse` |
| `POST` | `/api/v1/control/requery/rules/save` | 执行批量 save 动作 | 无 | `RequeryBatchActionResult` |
| `POST` | `/api/v1/control/requery/rules/flush` | 执行批量 flush 动作 | 无 | `RequeryBatchActionResult` |
| `GET` | `/api/v1/control/requery/stats/source_file_counts` | 统计 source_files 中的域名条数 | 无 | `RequerySourceFileCountResponse` |

说明：

- `rules/save` / `rules/flush` 任一目标失败时当前实现返回 `502`
- requery 的错误格式不是全局 `code/error`，而是 `status/message`

## 7. Upstream

### 7.1 推荐入口

| 方法 | 路径 | 说明 | 主要请求 | 主要返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/upstream/tags` | 读取运行中的上游插件 tag | 无 | `string[]` |
| `GET` | `/api/v1/upstream/config` | 读取完整上游配置 | 无 | `GlobalUpstreamOverrides` |
| `PUT` | `/api/v1/upstream/config` | 全量替换配置 | `UpstreamConfigReplaceRequest` | `{ "message": "..." }` |
| `POST` | `/api/v1/upstream/config` | 设置单个 plugin_tag 的上游列表 | `UpstreamConfigCompatRequest` | `{ "message": "..." }` |
| `POST` | `/api/v1/upstream/apply` | 重新加载已保存的上游配置 | 空体或 `{ "plugin_tag": "..." }` | `{ "message": "..." }` |
| `GET` | `/api/v1/upstream/items` | 查询某个 plugin_tag 的上游项 | `plugin_tag` 必填 | `UpstreamOverrideConfig[]` |
| `POST` | `/api/v1/upstream/items` | 新增一条上游项 | `UpstreamItemMutationRequest` | `201` + `{ "message": "..." }` |
| `PUT` | `/api/v1/upstream/items/{upstreamTag}` | 更新一条上游项 | `UpstreamItemMutationRequest` | `{ "message": "..." }` |
| `DELETE` | `/api/v1/upstream/items/{upstreamTag}` | 删除一条上游项 | `plugin_tag` 必填，`apply` 可选 | `{ "message": "..." }` |
| `GET` | `/api/v1/control/upstreams/health` | 上游健康聚合 | 无 | `UpstreamHealthOverview` |

### 7.2 兼容别名

| 方法 | 路径 | 状态 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/control/upstreams` | `compat` | 等价于 `/api/v1/upstream/config` |
| `PUT` | `/api/v1/control/upstreams` | `compat` | 等价于 `/api/v1/upstream/config` |
| `POST` | `/api/v1/control/upstreams` | `compat` | 等价于 `/api/v1/upstream/config` 的单 plugin 写法 |
| `GET` | `/api/v1/control/upstreams/tags` | `compat` | 等价于 `/api/v1/upstream/tags` |

说明：

- `PUT /api/v1/upstream/config` 使用 `{ "config": { "plugin_tag": [...] }, "apply": true }`
- `POST /api/v1/upstream/config` 使用 `{ "plugin_tag": "...", "upstreams": [...] }`
- `DELETE /api/v1/upstream/items/{upstreamTag}` 的 `plugin_tag` 走 query，不在 body

## 8. Rules

### 8.1 AdGuard 规则源

| 方法 | 路径 | 说明 | 主要返回 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/rules/adguard` | 列出广告规则源 | `RuleSourceItem[]` |
| `POST` | `/api/v1/rules/adguard` | 新建广告规则源 | `201` + `RuleSourceItem` |
| `PUT` | `/api/v1/rules/adguard/{id}` | 更新广告规则源 | `RuleSourceItem` |
| `DELETE` | `/api/v1/rules/adguard/{id}` | 删除广告规则源 | `RuleSourceDeleteResponse` |
| `POST` | `/api/v1/rules/adguard/update` | 刷新全部广告规则源 | `RuleSourceItem[]` |
| `POST` | `/api/v1/rules/adguard/{id}/update` | 刷新单个广告规则源 | `RuleSourceItem` |

### 8.2 Diversion 规则源

| 方法 | 路径 | 说明 | 主要返回 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/rules/diversion` | 列出在线分流规则源 | `RuleSourceItem[]` |
| `POST` | `/api/v1/rules/diversion` | 新建在线分流规则源 | `201` + `RuleSourceItem` |
| `PUT` | `/api/v1/rules/diversion/{id}` | 更新在线分流规则源 | `RuleSourceItem` |
| `DELETE` | `/api/v1/rules/diversion/{id}` | 删除在线分流规则源 | `RuleSourceDeleteResponse` |
| `POST` | `/api/v1/rules/diversion/update` | 刷新全部在线分流规则源 | `RuleSourceItem[]` |
| `POST` | `/api/v1/rules/diversion/{id}/update` | 刷新单个在线分流规则源 | `RuleSourceItem` |

说明：

- 写入时用 `RuleSourceWriteRequest`
- `behavior` 不作为写入参数，服务端会根据 `match_mode` 自动推导：
  - `adguard_native -> adguard`
  - `domain_set -> domain`
  - `ip_cidr_set -> ipcidr`
- `bindings` 是读取时返回的派生字段，不参与写入。
- `DELETE` 现在会返回文件清理结果；如果对应规则文件位于受管目录 `adguard/` 或 `diversion/` 下，且未被其他规则源复用，会一并删除本地文件。
- `PUT` 在规则源 `id/path` 变化后，也会自动清理旧的受管规则文件。
- 读取时用 `RuleSourceItem`
- `rule_count`、`last_updated`、`last_error` 为运行态衍生字段

## 9. Config / Update / System

| 方法 | 路径 | 说明 | 请求体 | 返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/config/info` | 当前配置目录和默认远程源 | 无 | `ConfigManagerInfo` |
| `POST` | `/api/v1/config/export` | 导出配置目录 ZIP | 可选 `ConfigManagerRequest` | `application/zip` |
| `POST` | `/api/v1/config/update_from_url` | 远程下载并覆盖当前配置 | 可选 `ConfigManagerRequest` | `ConfigUpdateFromURLResponse` |
| `GET` | `/api/v1/update/status` | 读取更新状态 | 无 | `UpdateStatus` |
| `POST` | `/api/v1/update/check` | 强制刷新更新状态 | 无 | `UpdateStatus` |
| `POST` | `/api/v1/update/apply` | 执行程序更新 | 可选 `UpdateApplyRequest` | `UpdateActionResponse` |
| `POST` | `/api/v1/system/restart` | 安排自重启 | 可选 `SystemRestartRequest` | `SystemRestartResponse` |

说明：

- `config/update_from_url` 成功后会在后台安排自重启
- `system/restart` 默认 `delay_ms=300`
- Windows 下重启接口可能返回 `501`

## 10. Runtime Stats / Cache / Lists / Memory / Misc

### 10.1 Runtime Stats

| 方法 | 路径 | 说明 | 返回 |
| --- | --- | --- | --- |
| `GET` | `/api/v1/cache/stats` | 聚合缓存统计 | `AggregatedCacheStatsResponse` |
| `GET` | `/api/v1/data/domain_stats` | 聚合域名统计 | `AggregatedDomainStatsResponse` |

### 10.2 Cache

| 方法 | 路径 | 说明 | 主要请求 | 主要返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/cache/{tag}/stats` | 单缓存实例统计 | 无 | `CacheStatsSnapshot` |
| `GET` | `/api/v1/cache/{tag}/entries` | 查询缓存条目 | `q,offset,limit` | `CacheEntriesResponse` |
| `POST` | `/api/v1/cache/{tag}/save` | 持久化缓存实例 | 无 | `TagMessageResponse` |
| `POST` | `/api/v1/cache/{tag}/flush` | 清空缓存实例 | 无 | `{ "message": "..." }` |
| `POST` | `/api/v1/cache/{tag}/purge_domain` | 按域名清理缓存 | `PurgeDomainRequest` | `PurgeDomainResponse` |

补充说明：

- 当前缓存策略真源文件为 `config/sub_config/cache_policies.yaml`
- `GET /api/v1/cache/stats` 和 `GET /api/v1/cache/{tag}/stats` 返回里的 `config.persist` 表示该缓存是否会做 dump + WAL 持久化
- `config.persist=false` 表示该缓存只保存在内存中
- `config.size` 表示最大缓存条目数，不是文件大小；超过后按 LRU 淘汰旧条目

### 10.3 Lists

| 方法 | 路径 | 说明 | 请求体 | 返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/lists/{tag}` | 查询可编辑列表 | `q,offset,limit` | `ListEntriesResponse` |
| `PUT` | `/api/v1/lists/{tag}` | 全量替换列表内容 | `ListReplaceRequest` | `ListReplaceResponse` |

### 10.4 Memory

| 方法 | 路径 | 说明 | 请求体 | 返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/memory/{tag}/stats` | 查询记忆池统计 | 无 | `DomainStatsSnapshot` |
| `GET` | `/api/v1/memory/{tag}/entries` | 查询记忆池条目 | `q,offset,limit` | `MemoryEntriesResponse` |
| `POST` | `/api/v1/memory/{tag}/save` | 持久化记忆池 | 无 | `TagMessageResponse` |
| `POST` | `/api/v1/memory/{tag}/flush` | 清空记忆池 | 无 | `TagMessageResponse` |
| `POST` | `/api/v1/memory/{tag}/verify` | 标记某域名已验证 | `VerifyMemoryRequest` | `VerifyMemoryResponse` |

补充说明：

- `my_fakeiplist` 这类记忆池不是 DNS 响应缓存
- 它们用于记录运行过程中观察到的域名决策结果，供统计、查看和规则候选发布使用

### 10.5 Misc

| 方法 | 路径 | 说明 | 请求 | 返回 |
| --- | --- | --- | --- | --- |
| `GET` | `/api/v1/reverse_lookup` | IP 反查域名 | `ip` 必填 | `ReverseLookupResponse` |

## 11. 字段级定义在哪里看

字段级 schema、全部 query 参数、兼容别名标记、响应类型和示例请直接查看：

- `config/ui/openapi.yaml`
- `/ui/api-docs.html`

这两处是当前重构后 API 的机器可消费真源；后续新增或删除接口时，应优先同步 OpenAPI，再同步本文档和矩阵文档。
