# 审计日志 SQLite 重构方案

## 1. 文档定位

本文档用于规划审计日志系统从“内存窗口 + `ndjson` 文件持久化”演进到“SQLite 主存储 + 内存摘要加速”的完整方案。

本文档只描述推荐设计、实施阶段和验收标准，不代表当前代码已经具备这些能力。

当前目标不是立刻重写全部审计逻辑，而是先把后续实现边界讲清楚，避免在落地时出现以下问题：

- 查询能力不足，后续又重新设计表结构
- 持久化与保留策略分散，出现重复实现
- 前端统计、分页、筛选接口与存储层耦合过深
- 重启恢复、容量治理、清理策略无法验证

## 2. 当前实现基线

当前仓库里的审计日志能力已经具备以下基础：

- 运行时采集：`coremain/audit.go`
- 审计 API：
  - `coremain/api_audit.go`
  - `coremain/api_audit_v2.go`
- 当前持久化：
  - `coremain/audit_persistence.go`
  - 目录：`config/audit_logs/`
  - 格式：`audit-YYYY-MM-DD.ndjson`

当前模型可以概括为：

- 内存层：保存最近一段审计日志，并直接支撑 UI 列表、排行和统计
- 磁盘层：按天追加写入 `ndjson`，用于重启恢复和短期历史保留

这套模型已经解决了“重启即丢失”问题，但仍存在明显上限：

- 历史分页和复杂筛选能力弱
- 读路径越复杂，越依赖全量扫描或内存窗口
- 排行和统计逻辑仍主要绑定内存数据结构
- 磁盘保留虽然可控，但不适合承载更复杂查询
- 未来如果增加更多筛选字段，会持续堆高文件扫描成本

## 3. 为什么推荐 SQLite

这里推荐的是：

- `SQLite` 作为审计日志主存储
- `内存摘要/热点窗口` 作为实时查询加速层

不建议直接走以下两种极端方案：

- 只保留内存：重启恢复和历史检索能力不足
- 只保留文本文件：排序、分页、过滤和统计扩展成本过高

选择 SQLite 的原因：

- 单机部署适配性好，不引入独立服务依赖
- 支持事务、索引、分页、排序、过滤
- 对“本地管理面板 + 本地日志数据库”这个场景足够合适
- 可逐步替换文件扫描，不需要一次性推翻现有 API

## 4. 设计目标

### 4.1 目标

- 保持当前 UI 查询速度和交互体验
- 让历史日志成为可持续扩展的持久数据，而不是仅靠内存窗口
- 支持更强的筛选、分页、排行和时间范围检索
- 将重启恢复从“回放文本文件”演进为“直接从数据库恢复”
- 保持现有 API 尽量兼容，避免前端同时大改

### 4.2 非目标

- 不把运行时日志抓取 `capture` 一并并入 SQLite
- 不在第一阶段就支持全文检索引擎级能力
- 不在第一阶段就把所有排行都改成纯 SQL 实时聚合
- 不引入外部数据库服务

## 5. 总体架构

推荐架构分三层：

### 5.1 写入层

- 审计采集协程仍然负责接收查询事件
- 每批审计记录进入统一写入缓冲
- 写入缓冲执行两类动作：
  - 更新内存摘要
  - 批量写入 SQLite

### 5.2 查询层

- 最近窗口列表：优先从内存窗口返回
- 实时概览和排行：优先从内存摘要返回
- 历史分页和深筛选：从 SQLite 查询

### 5.3 治理层

- 按天数清理过期行
- 按数据库文件大小限制做裁剪或 `VACUUM`
- 暴露状态、大小、最近清理时间等运维信息

## 6. 存储模型

### 6.1 数据库文件

建议数据库文件：

- `config/audit_logs/audit.db`

可选辅助文件：

- `config/audit_logs/audit.db-wal`
- `config/audit_logs/audit.db-shm`

建议启用：

- `WAL` 模式
- `NORMAL` 或 `FULL` 级别 `synchronous`

默认建议：

- `journal_mode = WAL`
- `synchronous = NORMAL`
- `temp_store = MEMORY`
- `busy_timeout = 3000`

### 6.2 主表

建议主表：`audit_logs`

字段建议：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | 主键 |
| `ts_unix_ms` | `INTEGER NOT NULL` | 查询时间，毫秒时间戳 |
| `query_name` | `TEXT NOT NULL` | 域名 |
| `query_type` | `INTEGER NOT NULL` | QTYPE |
| `client_ip` | `TEXT` | 客户端 IP |
| `elapsed_ms` | `INTEGER NOT NULL` | 查询耗时 |
| `rcode` | `INTEGER` | 返回码 |
| `has_error` | `INTEGER NOT NULL DEFAULT 0` | 是否出错 |
| `answer_ips_json` | `TEXT` | A/AAAA 结果 JSON |
| `cnames_json` | `TEXT` | CNAME 结果 JSON |
| `domain_set` | `TEXT` | 命中的规则集说明 |
| `resp_ip` | `TEXT` | 兼容旧筛选，首个主要响应 IP |
| `q_mark` | `INTEGER` | 规则链标记 |
| `extra_json` | `TEXT` | 其余低频字段，避免首版表过宽 |

设计原则：

- 高频筛选字段单独列出
- 低频展示字段可先进入 `extra_json`
- 避免首版为了“完整无损映射”直接堆太多列

### 6.3 聚合快照表

不建议第一阶段把所有排行都实时 `GROUP BY` 主表。

推荐增加轻量聚合表，用于快速恢复和降低冷启动成本：

- `audit_rollup_daily`
- `audit_rollup_hourly`

可选字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `bucket_start_unix` | `INTEGER` | 小时/天时间桶 |
| `metric_name` | `TEXT` | 指标名 |
| `metric_key` | `TEXT` | 维度键，如域名、客户端、domain_set |
| `metric_value` | `INTEGER` | 计数 |

这类表第一阶段不是必须，但建议在设计上预留。

## 7. 索引设计

建议首版建立以下索引：

- `idx_audit_logs_ts` on `audit_logs(ts_unix_ms DESC)`
- `idx_audit_logs_query_name_ts` on `audit_logs(query_name, ts_unix_ms DESC)`
- `idx_audit_logs_client_ip_ts` on `audit_logs(client_ip, ts_unix_ms DESC)`
- `idx_audit_logs_domain_set_ts` on `audit_logs(domain_set, ts_unix_ms DESC)`
- `idx_audit_logs_resp_ip_ts` on `audit_logs(resp_ip, ts_unix_ms DESC)`
- `idx_audit_logs_elapsed_ts` on `audit_logs(elapsed_ms DESC, ts_unix_ms DESC)`

说明：

- `query_name`、`client_ip`、`domain_set` 是当前 UI 的高频筛选条件
- `elapsed_ms` 用于慢查询排行
- `ts_unix_ms` 是所有分页和保留清理的基础

不建议首版建立过多组合索引，否则写入成本会上升过快。

## 8. 内存层如何保留

SQLite 更适合作为主存储，但不建议彻底移除内存层。

建议内存只保留两类结构：

### 8.1 最近窗口

- 例如最近 `5000 ~ 20000` 条
- 用于 UI 的最近日志、快速刷新和首屏列表

### 8.2 实时摘要

至少包括：

- 总请求数
- 平均耗时
- `top domain`
- `top client`
- `top domain_set`
- `slowest top N`

原因：

- 这些信息如果每次都从 SQLite 全量聚合，前端体验会明显变差
- 内存摘要可以在写入路径增量更新，读路径成本更低

一句话：

- `SQLite` 负责“存”
- `内存摘要` 负责“快”

## 9. 写入路径设计

推荐写入流程：

1. 审计事件进入 `AuditCollector`
2. 先更新内存窗口和内存摘要
3. 再将日志加入 SQLite 写入批次
4. 批量写入事务提交
5. 定期触发轻量清理和状态更新

关键要求：

- 不要每条日志单独开事务
- 写入失败不能阻塞主查询路径太久
- 必须有丢写告警和指标

建议参数：

- 批量大小：`128 ~ 512`
- 批量刷新间隔：`100 ~ 500ms`

## 10. 读取路径设计

### 10.1 `/api/v2/audit/logs`

建议规则：

- 无复杂筛选且请求最近第一页时：
  - 优先用内存窗口
- 其余情况：
  - 走 SQLite

这样做的原因：

- 最近第一页是最常见查询
- 深分页和历史筛选更适合数据库

### 10.2 `/api/v2/audit/stats`

默认直接读内存摘要。

### 10.3 排行接口

首版建议：

- 默认读内存摘要
- 如未来需要“指定时间范围排行”，再扩展 SQLite 聚合查询

## 11. 保留与清理策略

建议保留模型：

- 内存：按条数
- 磁盘：按天数 + 按数据库文件大小

### 11.1 天数保留

例如：

- `retention_days = 30`

清理逻辑：

- 删除 `ts_unix_ms < now - retention_days` 的行

### 11.2 大小保留

例如：

- `max_db_size_mb = 10`

清理逻辑建议：

1. 先执行按天数删除
2. 如果数据库文件仍超限：
   - 按时间从旧到新继续删最老行
3. 需要时再执行 `VACUUM` 或增量压缩

注意：

- SQLite 删除行后，文件大小不会立刻线性变小
- 所以必须设计明确的压缩策略，而不是只删行

建议：

- 日常使用 `auto_vacuum = INCREMENTAL`
- 低频任务中执行 `incremental_vacuum`
- 大规模裁剪后再评估是否做一次完整 `VACUUM`

## 12. API 兼容策略

目标是尽量不改前端接口语义。

### 12.1 保持兼容的接口

以下接口建议保持路径不变：

- `GET /api/v1/audit/status`
- `GET /api/v1/audit/capacity`
- `POST /api/v1/audit/capacity`
- `POST /api/v1/audit/clear`
- `GET /api/v2/audit/stats`
- `GET /api/v2/audit/rank/domain`
- `GET /api/v2/audit/rank/client`
- `GET /api/v2/audit/rank/domain_set`
- `GET /api/v2/audit/rank/slowest`
- `GET /api/v2/audit/logs`

### 12.2 建议新增字段

`GET /api/v1/audit/capacity` 可扩展返回：

```json
{
  "memory_entries": 100000,
  "retention_days": 30,
  "max_disk_size_mb": 10,
  "db_path": "config/audit_logs/audit.db",
  "current_disk_size_bytes": 1048576,
  "current_row_count": 12345,
  "storage_engine": "sqlite"
}
```

### 12.3 清空接口

建议将清空语义拆清楚：

- 默认清空内存窗口 + SQLite 历史
- 后续可选支持：
  - 仅清空内存窗口
  - 仅裁剪历史

但首版可以保留现有“一键清空全部”的行为。

## 13. 迁移方案

### Phase 0：设计确认

- 确认表结构、索引、保留策略
- 确认是否保留 `ndjson` 兼容导出能力

### Phase 1：双写不切读

- 现有内存 + `ndjson` 继续工作
- 新增 SQLite 双写
- 前端和 API 仍读旧逻辑

目的：

- 先验证写入性能和文件大小控制
- 不把读路径一起改坏

### Phase 2：读路径灰度切换

- 最近窗口仍走内存
- 历史分页逐步切 SQLite
- 保持旧返回结构不变

### Phase 3：排行与统计收敛

- 继续以内存摘要为主
- 补齐从 SQLite 重建摘要的恢复逻辑

### Phase 4：移除 `ndjson` 主路径

- SQLite 成为唯一主持久化
- `ndjson` 降级为导出格式或调试能力

## 14. 回滚方案

必须支持可逆。

建议回滚路径：

- 保留开关：
  - `audit.storage_engine = ndjson | sqlite`
- SQLite 失败时：
  - 回退为当前 `ndjson` 方案
- 双写阶段不得删除旧文件逻辑

回滚标准：

- 写入错误率异常
- 查询延迟明显恶化
- 数据量增长后数据库治理不可控

## 15. 性能与可靠性要求

建议首版验收基线：

- 高频查询场景下，审计写入不能显著拉高 DNS 主路径延迟
- 最近日志首屏查询体验不低于当前实现
- 历史分页在常见数据量下稳定返回
- 服务重启后可恢复历史日志
- 数据库损坏时能够明确告警并安全降级

建议重点验证：

- 10 万条、100 万条日志下的分页性能
- 多筛选组合查询性能
- 数据库膨胀与清理效果
- WAL 文件增长控制

## 16. 风险点

### 16.1 写入路径复杂化

如果事务批次、锁粒度或错误处理设计不好，容易影响主查询路径。

### 16.2 统计口径双源不一致

如果内存摘要和 SQLite 恢复逻辑不一致，排行和历史可能出现口径偏差。

### 16.3 清理后空间不立即回收

SQLite 的“删行不等于立刻缩文件”必须明确处理。

### 16.4 过度 SQL 化

不建议首版把所有统计都做成实时 SQL 聚合，否则会把本来很快的 UI 首屏拖慢。

## 17. 推荐实施顺序

推荐按这个顺序落地：

1. 抽象 `AuditStorage` 接口
2. 新增 SQLite 写入器和查询器
3. 先做双写
4. 保持内存摘要不变
5. 将历史分页切到 SQLite
6. 最后再决定是否移除 `ndjson`

## 18. 最终建议

对当前项目来说，最合理的目标形态不是“纯 SQLite”，而是：

- `SQLite`：持久主存储
- `内存窗口`：最近日志快速展示
- `内存摘要`：首屏统计和排行

这样可以同时满足：

- 查询快
- 重启不丢
- 历史可查
- 前端少改
- 后续易扩展

如果后续确认要实现，建议优先做 `Phase 1` 和 `Phase 2`，不要一开始就把所有排行和统计都迁到数据库里。
