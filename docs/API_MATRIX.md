# API Matrix

文档真源：

- Markdown 说明：[API_REFERENCE.md](./API_REFERENCE.md)
- 运行态 Swagger：`http://127.0.0.1:9099/ui/api-docs.html`
- 运行态 OpenAPI：`http://127.0.0.1:9099/ui/openapi.yaml`

状态说明：

- `stable`：推荐直接对接
- `compat`：兼容别名，建议新代码迁移

## Capture

| 模块 | 方法 | 路径 | 稳定性 | 返回 |
| --- | --- | --- | --- | --- |
| Capture | `POST` | `/api/v1/capture/start` | `stable` | `CaptureStartResponse` |
| Capture | `GET` | `/api/v1/capture/logs` | `stable` | `CapturedLogsResponse` |

## Audit

| 模块 | 方法 | 路径 | 稳定性 | 返回 |
| --- | --- | --- | --- | --- |
| Audit | `GET` | `/api/v3/audit/overview` | `stable` | `AuditOverview` |
| Audit | `GET` | `/api/v3/audit/timeseries` | `stable` | `AuditTimeseriesPoint[]` |
| Audit | `GET` | `/api/v3/audit/rank/domain` | `stable` | `AuditRankItem[]` |
| Audit | `GET` | `/api/v3/audit/rank/client` | `stable` | `AuditRankItem[]` |
| Audit | `GET` | `/api/v3/audit/rank/domain_set` | `stable` | `AuditRankItem[]` |
| Audit | `GET` | `/api/v3/audit/logs` | `stable` | `AuditLogsResponse` |
| Audit | `POST` | `/api/v3/audit/logs/search` | `stable` | `AuditLogsResponse` |
| Audit | `GET` | `/api/v3/audit/logs/slow` | `stable` | `AuditLog[]` |
| Audit | `GET` | `/api/v3/audit/settings` | `stable` | `AuditSettingsResponse` |
| Audit | `PUT` | `/api/v3/audit/settings` | `stable` | `AuditSettingsResponse` |
| Audit | `POST` | `/api/v3/audit/clear` | `stable` | `MessageResponse` |

## Runtime / Overrides / Switches

| 模块 | 方法 | 路径 | 稳定性 | 返回 |
| --- | --- | --- | --- | --- |
| Runtime | `GET` | `/api/v1/control/summary` | `stable` | `RuntimeSummaryResponse` |
| Runtime | `GET` | `/api/v1/control/health` | `stable` | `RuntimeHealthResponse` |
| Runtime | `GET` | `/api/v1/control/events` | `stable` | `SystemEventEntry[]` |
| Runtime | `GET` | `/api/v1/control/shunt/explain` | `stable` | `ShuntExplainResult` |
| Runtime | `GET` | `/api/v1/control/shunt/conflicts` | `stable` | `ShuntConflictsResponse` |
| Overrides | `GET` | `/api/v1/overrides` | `stable` | `GlobalOverridesResponse` |
| Overrides | `POST` | `/api/v1/overrides` | `stable` | `MessageResponse` |
| Overrides | `GET` | `/api/v1/control/overrides` | `compat` | `GlobalOverridesResponse` |
| Overrides | `POST` | `/api/v1/control/overrides` | `compat` | `MessageResponse` |
| Overrides | `GET` | `/api/v1/control/clientname` | `stable` | `map[string]string` |
| Overrides | `PUT` | `/api/v1/control/clientname` | `stable` | `map[string]string` |
| Switches | `GET` | `/api/v1/control/switches` | `stable` | `SwitchState[]` |
| Switches | `GET` | `/api/v1/control/switches/{name}` | `stable` | `SwitchState` |
| Switches | `PUT` | `/api/v1/control/switches/{name}` | `stable` | `SwitchState` |

## Requery

| 模块 | 方法 | 路径 | 稳定性 | 返回 |
| --- | --- | --- | --- | --- |
| Requery | `GET` | `/api/v1/control/requery` | `stable` | `RequeryConfig` |
| Requery | `GET` | `/api/v1/control/requery/status` | `stable` | `RequeryStatusSnapshot` |
| Requery | `GET` | `/api/v1/control/requery/summary` | `stable` | `RequerySummaryResponse` |
| Requery | `GET` | `/api/v1/control/requery/jobs` | `stable` | `RequeryJob[]` |
| Requery | `GET` | `/api/v1/control/requery/runs` | `stable` | `RequeryRun[]` |
| Requery | `GET` | `/api/v1/control/requery/checkpoints` | `stable` | `RequeryCheckpoint[]` |
| Requery | `POST` | `/api/v1/control/requery/trigger` | `stable` | `RequeryTriggerResponse` |
| Requery | `POST` | `/api/v1/control/requery/enqueue` | `stable` | `202` + `RequeryEnqueueResponse` |
| Requery | `POST` | `/api/v1/control/requery/cancel` | `stable` | `StatusMessageResponse` |
| Requery | `POST` | `/api/v1/control/requery/scheduler/config` | `stable` | `StatusMessageResponse` |
| Requery | `POST` | `/api/v1/control/requery/rules/save` | `stable` | `RequeryBatchActionResult` |
| Requery | `POST` | `/api/v1/control/requery/rules/flush` | `stable` | `RequeryBatchActionResult` |
| Requery | `GET` | `/api/v1/control/requery/stats/source_file_counts` | `stable` | `RequerySourceFileCountResponse` |

## Upstream

| 模块 | 方法 | 路径 | 稳定性 | 返回 |
| --- | --- | --- | --- | --- |
| Upstream | `GET` | `/api/v1/upstream/tags` | `stable` | `string[]` |
| Upstream | `GET` | `/api/v1/upstream/config` | `stable` | `GlobalUpstreamOverrides` |
| Upstream | `PUT` | `/api/v1/upstream/config` | `stable` | `MessageResponse` |
| Upstream | `POST` | `/api/v1/upstream/config` | `stable` | `MessageResponse` |
| Upstream | `POST` | `/api/v1/upstream/apply` | `stable` | `MessageResponse` |
| Upstream | `GET` | `/api/v1/upstream/items` | `stable` | `UpstreamOverrideConfig[]` |
| Upstream | `POST` | `/api/v1/upstream/items` | `stable` | `201` + `MessageResponse` |
| Upstream | `PUT` | `/api/v1/upstream/items/{upstreamTag}` | `stable` | `MessageResponse` |
| Upstream | `DELETE` | `/api/v1/upstream/items/{upstreamTag}` | `stable` | `MessageResponse` |
| Upstream | `GET` | `/api/v1/control/upstreams` | `compat` | `GlobalUpstreamOverrides` |
| Upstream | `PUT` | `/api/v1/control/upstreams` | `compat` | `MessageResponse` |
| Upstream | `POST` | `/api/v1/control/upstreams` | `compat` | `MessageResponse` |
| Upstream | `GET` | `/api/v1/control/upstreams/health` | `stable` | `UpstreamHealthOverview` |
| Upstream | `GET` | `/api/v1/control/upstreams/tags` | `compat` | `string[]` |

## Rules

| 模块 | 方法 | 路径 | 稳定性 | 返回 |
| --- | --- | --- | --- | --- |
| Rules | `GET` | `/api/v1/rules/adguard` | `stable` | `RuleSourceItem[]` |
| Rules | `POST` | `/api/v1/rules/adguard` | `stable` | `201` + `RuleSourceItem` |
| Rules | `PUT` | `/api/v1/rules/adguard/{id}` | `stable` | `RuleSourceItem` |
| Rules | `DELETE` | `/api/v1/rules/adguard/{id}` | `stable` | `204 No Content` |
| Rules | `POST` | `/api/v1/rules/adguard/update` | `stable` | `RuleSourceItem[]` |
| Rules | `POST` | `/api/v1/rules/adguard/{id}/update` | `stable` | `RuleSourceItem` |
| Rules | `GET` | `/api/v1/rules/diversion` | `stable` | `RuleSourceItem[]` |
| Rules | `POST` | `/api/v1/rules/diversion` | `stable` | `201` + `RuleSourceItem` |
| Rules | `PUT` | `/api/v1/rules/diversion/{id}` | `stable` | `RuleSourceItem` |
| Rules | `DELETE` | `/api/v1/rules/diversion/{id}` | `stable` | `204 No Content` |
| Rules | `POST` | `/api/v1/rules/diversion/update` | `stable` | `RuleSourceItem[]` |
| Rules | `POST` | `/api/v1/rules/diversion/{id}/update` | `stable` | `RuleSourceItem` |

## Config / Update / System / Misc

| 模块 | 方法 | 路径 | 稳定性 | 返回 |
| --- | --- | --- | --- | --- |
| Config | `GET` | `/api/v1/config/info` | `stable` | `ConfigManagerInfo` |
| Config | `POST` | `/api/v1/config/export` | `stable` | `application/zip` |
| Config | `POST` | `/api/v1/config/update_from_url` | `stable` | `ConfigUpdateFromURLResponse` |
| Update | `GET` | `/api/v1/update/status` | `stable` | `UpdateStatus` |
| Update | `POST` | `/api/v1/update/check` | `stable` | `UpdateStatus` |
| Update | `POST` | `/api/v1/update/apply` | `stable` | `UpdateActionResponse` |
| System | `POST` | `/api/v1/system/restart` | `stable` | `SystemRestartResponse` |
| Misc | `GET` | `/api/v1/reverse_lookup` | `stable` | `ReverseLookupResponse` |

## RuntimeStats / Cache / Lists / Memory

| 模块 | 方法 | 路径 | 稳定性 | 返回 |
| --- | --- | --- | --- | --- |
| RuntimeStats | `GET` | `/api/v1/cache/stats` | `stable` | `AggregatedCacheStatsResponse` |
| RuntimeStats | `GET` | `/api/v1/data/domain_stats` | `stable` | `AggregatedDomainStatsResponse` |
| Cache | `GET` | `/api/v1/cache/{tag}/stats` | `stable` | `CacheStatsSnapshot` |
| Cache | `GET` | `/api/v1/cache/{tag}/entries` | `stable` | `CacheEntriesResponse` |
| Cache | `POST` | `/api/v1/cache/{tag}/save` | `stable` | `TagMessageResponse` |
| Cache | `POST` | `/api/v1/cache/{tag}/flush` | `stable` | `MessageResponse` |
| Cache | `POST` | `/api/v1/cache/{tag}/purge_domain` | `stable` | `PurgeDomainResponse` |
| Lists | `GET` | `/api/v1/lists/{tag}` | `stable` | `ListEntriesResponse` |
| Lists | `PUT` | `/api/v1/lists/{tag}` | `stable` | `ListReplaceResponse` |
| Memory | `GET` | `/api/v1/memory/{tag}/stats` | `stable` | `DomainStatsSnapshot` |
| Memory | `GET` | `/api/v1/memory/{tag}/entries` | `stable` | `MemoryEntriesResponse` |
| Memory | `POST` | `/api/v1/memory/{tag}/save` | `stable` | `TagMessageResponse` |
| Memory | `POST` | `/api/v1/memory/{tag}/flush` | `stable` | `TagMessageResponse` |
| Memory | `POST` | `/api/v1/memory/{tag}/verify` | `stable` | `VerifyMemoryResponse` |
