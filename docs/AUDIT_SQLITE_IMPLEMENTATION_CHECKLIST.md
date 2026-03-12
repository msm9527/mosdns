# 审计日志 SQLite 实施清单

## 1. 文档定位

本文档是 [AUDIT_SQLITE_REFACTOR_PLAN.md](/Users/doumao/code/github/mosdns/docs/AUDIT_SQLITE_REFACTOR_PLAN.md) 的实施版清单。

目标不是重复设计，而是把后续开发拆成可以直接执行、验证和回滚的任务列表。

## 2. 实施原则

- 保持现有 API 路径不变，优先兼容前端
- 先双写，再切读，最后再考虑移除 `ndjson`
- SQLite 只接管持久化与历史查询，不直接替代内存摘要
- 每个阶段都必须有验收和回滚方案

## 3. Phase 0：预备工作

### 3.1 存储抽象

- 定义 `AuditStorage` 接口
- 抽离当前 `ndjson` 持久化实现为 `NdjsonAuditStorage`
- 预留 `SQLiteAuditStorage`

建议接口最小集合：

- `Open() error`
- `Close() error`
- `WriteBatch([]AuditLog) error`
- `QueryLogs(params) (result, error)`
- `CountLogs(params) (int64, error)`
- `DeleteBefore(ts int64) error`
- `GetDiskUsageBytes() (int64, error)`
- `Clear() error`

### 3.2 配置项准备

- 新增：
  - `storage_engine`
  - `sqlite_path`
  - `max_db_size_mb`
- 保留：
  - `memory_entries`
  - `retention_days`

建议默认值：

- `storage_engine = ndjson`
- `sqlite_path = config/audit_logs/audit.db`
- `memory_entries = 100000`
- `retention_days = 30`
- `max_db_size_mb = 10`

### 3.3 验收

- 现有 `ndjson` 路径不受影响
- 新配置项未启用时行为完全等同当前版本

### 3.4 回滚

- 不启用 `sqlite` 引擎即可回到现状

## 4. Phase 1：SQLite 写入器

### 4.1 数据库初始化

- 新建 SQLite 打开逻辑
- 初始化 pragma：
  - `journal_mode=WAL`
  - `synchronous=NORMAL`
  - `temp_store=MEMORY`
  - `busy_timeout=3000`
- 初始化主表 `audit_logs`
- 初始化基础索引

### 4.2 批量写入

- 把当前 `AuditCollector` 的批处理接到 SQLite 写入器
- 批量事务写入，不允许逐条事务
- 写入失败时记录告警与指标

### 4.3 指标

建议新增：

- `audit_sqlite_write_total`
- `audit_sqlite_write_fail_total`
- `audit_sqlite_write_duration_seconds`
- `audit_sqlite_queue_len`

### 4.4 验收

- SQLite 文件可正常生成
- 高频写入下服务不阻塞
- 审计日志能持续写入 SQLite

### 4.5 回滚

- 关闭 `sqlite` 引擎或关闭双写开关

## 5. Phase 2：双写

### 5.1 双写模型

- 继续保留当前 `ndjson` 写入
- 同时把相同批次写入 SQLite
- 读路径仍然全部走当前逻辑

### 5.2 对账工具

- 新增简单校验命令或调试接口
- 对比最近 N 条：
  - `ndjson` 恢复数量
  - SQLite 行数
  - 关键字段一致性

### 5.3 验收

- 双写期间前端无感
- SQLite 行数与 `ndjson` 近似一致
- 重启后当前逻辑仍能恢复日志

### 5.4 回滚

- 停止 SQLite 写入，保留 `ndjson`

## 6. Phase 3：SQLite 历史查询

### 6.1 切读范围

先只切这部分：

- `/api/v2/audit/logs`
  - 深分页
  - 带筛选条件
  - 非第一页最近窗口

仍保留：

- 最近第一页优先走内存窗口
- `/api/v2/audit/stats` 继续走内存摘要
- 排行接口继续走内存摘要

### 6.2 查询能力

首版支持：

- `page`
- `limit`
- `domain`
- `answer_ip`
- `cname`
- `client_ip`
- `q`
- `exact`
- 时间倒序分页

### 6.3 验收

- 接口返回结构与当前一致
- 深分页明显不依赖内存窗口
- 常见筛选组合查询可接受

### 6.4 回滚

- 读路径开关切回旧逻辑

## 7. Phase 4：恢复路径切换

### 7.1 启动恢复

- 当前重启恢复从 `ndjson` 回放
- 这一阶段改成：
  - 优先从 SQLite 恢复最近窗口
  - 恢复内存摘要

### 7.2 内存恢复策略

- 恢复最近 N 条到内存窗口
- 恢复：
  - 总请求数
  - 平均耗时
  - top domain/client/domain_set
  - slowest top N

### 7.3 验收

- 服务重启后首页统计正常
- 最近日志和历史分页都能查到
- 不再依赖 `ndjson` 回放作为主恢复链路

### 7.4 回滚

- 启动恢复切回 `ndjson`

## 8. Phase 5：保留与裁剪

### 8.1 天数清理

- 定时删除 `ts_unix_ms < now - retention_days` 的行

### 8.2 大小治理

- 监控数据库大小
- 超过 `max_db_size_mb` 时继续删除最老行
- 结合：
  - `auto_vacuum=INCREMENTAL`
  - 周期性 `incremental_vacuum`

### 8.3 运维状态

- `GET /api/v1/audit/capacity` 增加：
  - `storage_engine`
  - `db_path`
  - `current_row_count`
  - `current_disk_size_bytes`

### 8.4 验收

- 天数和大小限制都能生效
- 数据库不会无限膨胀
- 清理后查询能力正常

### 8.5 回滚

- 关闭 SQLite 清理任务，改回 `ndjson` 裁剪

## 9. Phase 6：移除 `ndjson` 主路径

### 9.1 条件

必须先满足：

- 双写稳定
- 历史分页稳定
- 启动恢复稳定
- 裁剪稳定
- 指标和错误处理完整

### 9.2 动作

- SQLite 成为默认主存储
- `ndjson` 降级为：
  - 导出能力
  - 调试兜底
  - 或完全移除

### 9.3 验收

- 默认部署不再依赖 `ndjson`
- API 与前端无感

### 9.4 回滚

- `storage_engine` 切回 `ndjson`

## 10. 代码拆分建议

建议新增或拆分以下文件：

- `coremain/audit_storage.go`
- `coremain/audit_storage_ndjson.go`
- `coremain/audit_storage_sqlite.go`
- `coremain/audit_storage_sqlite_query.go`
- `coremain/audit_storage_sqlite_retention.go`
- `coremain/audit_storage_sqlite_test.go`

现有文件建议保留职责：

- `coremain/audit.go`
  - 采集、内存窗口、内存摘要
- `coremain/api_audit.go`
  - 存储配置与清理接口
- `coremain/api_audit_v2.go`
  - 查询与排行接口

## 11. 测试清单

### 11.1 单元测试

- SQLite 初始化
- 表结构创建
- 批量写入
- 常见筛选查询
- 深分页
- 按天数删除
- 按大小裁剪
- 启动恢复最近窗口

### 11.2 集成测试

- 连续写入后重启恢复
- 最近第一页走内存，历史分页走 SQLite
- 切换 `storage_engine` 的兼容性
- 清空日志后 SQLite 与内存一致

### 11.3 压测

- 10 万条日志查询
- 100 万条日志查询
- 高频写入下 DNS 主路径延迟变化
- WAL 文件增长与回收效果

## 12. 风险控制

### 12.1 必须提前做的保护

- SQLite 写入失败不能卡死 DNS 主路径
- 必须有写入错误计数和告警日志
- 必须保留回退开关

### 12.2 不建议首版做的事

- 所有排行全改 SQL 实时聚合
- 首版就引入全文检索
- 直接删除 `ndjson` 逻辑

## 13. 实施顺序建议

推荐实际开发顺序：

1. 抽象 `AuditStorage`
2. 做 SQLite 初始化和写入
3. 上双写
4. 做历史分页切读
5. 做恢复切换
6. 做保留与裁剪
7. 最后再考虑移除 `ndjson`

## 14. 交付标准

达到以下条件，才算这次重构真正完成：

- 前端无明显体验退化
- 重启后日志可恢复
- 历史分页不依赖大内存窗口
- 保留天数和大小都可控
- 出问题时可一键切回旧方案
