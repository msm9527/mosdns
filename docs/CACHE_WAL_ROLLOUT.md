# Cache WAL 灰度发布与回滚手册

## 适用范围

- 缓存插件：`type: cache`
- 配置文件：`config/sub_config/cache.yaml`
- 运行态接口：`/api/v1/cache/{cache_tag}/stats`

本手册用于指导当前缓存实例从仅 snapshot 模式切换到 `snapshot + WAL` 模式，并提供观测与回滚步骤。

## 当前默认配置

当前仓库中的缓存实例已经按生产推荐方式配置：

- 保留 `dump_file` / `dump_interval` 作为 checkpoint
- 追加 `wal_file` / `wal_sync_interval` 作为增量恢复链路

示例：

```yaml
- tag: cache_all
  type: cache
  args:
    dump_file: cache/cache_all.dump
    wal_file: cache/cache_all.wal
    dump_interval: 36000
    wal_sync_interval: 1
```

## 发布前检查

1. 确认每个缓存实例的 `dump_file` 和 `wal_file` 路径独立，不能复用同一 `.wal` 文件。
2. 确认运行用户对 `cache/` 目录具备创建、写入、重命名权限。
3. 确认现网监控已能访问 `/api/v1/cache/{cache_tag}/stats`。
4. 确认 UI 已切到 `/stats`，不要再用 `/metrics` 文本推导缓存状态。
5. 变更前保留现有 `.dump` 文件，避免同时丢失 snapshot 基线和 WAL 增量。

## 建议灰度顺序

建议按缓存职责分批启用，而不是一次性覆盖全部实例：

1. 低风险实例：`cache_node`、`cache_cnmihomo`
2. 中风险实例：`cache_cn`、`cache_google`、`cache_google_node`
3. 高流量入口实例：`cache_all`、`cache_all_noleak`

灰度方法：

1. 第一批实例保留 `wal_file` / `wal_sync_interval`，其余实例先移除这两个字段。
2. 发布并重启后，观察至少一个 `dump_interval` 周期内的运行态。
3. 确认 WAL 回放、snapshot 保存、命中率和 lazy update 没有异常后，再放开下一批。

## 发布后观测点

以 `GET /api/v1/cache/{cache_tag}/stats` 为准，重点看以下字段：

也可以直接使用仓库脚本：

```bash
python -X utf8 "scripts/check_cache_stats.py" --base-url "http://127.0.0.1:9099" --require-wal --strict
```

如果只想检查部分实例：

```bash
python -X utf8 "scripts/check_cache_stats.py" \
  --base-url "http://127.0.0.1:9099" \
  --tag "cache_all" \
  --tag "cache_all_noleak" \
  --strict
```

### 基础状态

- `snapshot_file`
- `wal_file`
- `backend_size`
- `l1_size`
- `updated_keys`

### 计数器

- `counters.query_total`
- `counters.hit_total`
- `counters.l1_hit_total`
- `counters.l2_hit_total`
- `counters.lazy_hit_total`
- `counters.lazy_update_total`
- `counters.lazy_update_dropped_total`

### 最近操作状态

- `last_dump.status`
- `last_load.status`
- `last_wal_replay.status`
- `last_dump.at`
- `last_load.at`
- `last_wal_replay.at`
- `last_dump.entries`
- `last_load.entries`
- `last_wal_replay.entries`
- `last_dump.error`
- `last_load.error`
- `last_wal_replay.error`

### 正常状态判定

- 首次启动后，`last_load.status` 应为 `ok` 或保持 `not_run`
- 启用 WAL 的实例在重启恢复后，`last_wal_replay.status` 应为 `ok` 或 `not_run`
- 周期保存成功后，`last_dump.status` 应为 `ok`
- `backend_size` 和 `l1_size` 应随流量增长而稳定变化，不应持续异常清零
- `lazy_update_dropped_total` 不应在短时间内持续异常上升

## 异常处理

### 场景 1：WAL 回放失败

现象：

- `last_wal_replay.status = error`
- `last_wal_replay.error` 有具体错误信息

处理：

1. 先保留当前 `.dump` 文件。
2. 记录失败实例的 `.wal` 文件路径。
3. 删除该实例配置中的 `wal_file` / `wal_sync_interval`。
4. 重启服务，先回到 snapshot-only 模式。
5. 待确认问题根因后，再重新启用 WAL。

### 场景 2：周期 dump 失败

现象：

- `last_dump.status = error`

处理：

1. 检查 `cache/` 目录权限、磁盘空间、挂载状态。
2. 确认 snapshot 文件路径未被外部程序占用。
3. 问题未修复前，不要删除现有 `.dump` 文件。
4. 如需止损，可先移除 `wal_file` / `wal_sync_interval`，保留 snapshot 链路。

### 场景 3：UI 显示异常但缓存功能正常

现象：

- DNS 命中正常，但面板为空或字段缺失

处理：

1. 直接访问 `/api/v1/cache/{cache_tag}/stats` 确认接口返回。
2. 若 `/stats` 正常，优先检查前端缓存标签是否与插件 `tag` 一致。
3. 不要回退到解析 `/metrics` 文本的旧方案。

## 回滚步骤

回滚目标：退回仅 snapshot 模式，不影响现有缓存插件 tag 和 dump 恢复链路。

1. 在目标实例中删除 `wal_file`。
2. 在目标实例中删除 `wal_sync_interval`。
3. 保留 `dump_file` 和 `dump_interval`。
4. 重启服务。
5. 用 `/api/v1/cache/{cache_tag}/stats` 确认：
   - `wal_file` 为空
   - `last_wal_replay.status` 不再作为必要观测项
   - `last_dump.status` 正常

## 最小发布检查清单

- [ ] 已完成配置变更备份
- [ ] 已确认 `cache/` 目录写权限
- [ ] 已确认灰度实例清单
- [ ] 已确认 `/api/v1/cache/{cache_tag}/stats` 可访问
- [ ] 已确认 UI 已切换到 `/stats`
- [ ] 已记录回滚人、回滚条件和窗口时间

## 关联文档

- [API 接口文档](./API_REFERENCE.md)
- [缓存系统重构与优化计划](./CACHE_REFACTOR_PLAN.md)
- [缓存巡检脚本](../scripts/check_cache_stats.py)
