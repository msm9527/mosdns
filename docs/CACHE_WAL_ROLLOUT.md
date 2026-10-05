# Cache WAL 灰度发布与回滚手册

## 适用范围

- 缓存插件：`type: cache`
- 配置文件：`config/sub_config/21-data-cache-upstreams.yaml` + `config/sub_config/cache_policies.yaml`
- 运行态接口：`/api/v1/cache/{cache_tag}/stats`

本手册用于指导当前“可持久化响应缓存实例”启用或回滚 `snapshot + WAL` 模式，并提供观测步骤。

## 当前默认配置

当前仓库中的缓存实例按职责分为两类：

- 默认持久化缓存为 `cache_main`
- 默认非持久化缓存包括 `cache_branch_domestic`、`cache_branch_foreign`、`cache_branch_foreign_ecs`、`cache_fakeip_domestic`、`cache_fakeip_proxy`、`cache_probe`

其中只有持久化缓存需要关注 `dump_file` / `wal_file`。

示例：

```yaml
response:
  cache_main:
    persist: true
    dump_file: db/cache/cache_main.dump
    dump_interval: 3600
    wal_sync_interval: 60
```

## 发布前检查

1. 确认每个缓存实例的 `dump_file` 和 `wal_file` 路径独立，不能复用同一 `.wal` 文件。
2. 确认运行用户对 `cache/` 目录具备创建、写入、重命名权限。
3. 确认现网监控已能访问 `/api/v1/cache/{cache_tag}/stats`。
4. 确认 UI 已切到 `/stats`，不要再用 `/metrics` 文本推导缓存状态。
5. 停止写入后成对备份每个实例的 `.dump` 和 `.wal`，并保留对应二进制和配置。备份期间不能让 checkpoint 轮换其中一个文件。

## WAL v2 格式与恢复合同

持久化实例使用现有 `dump_file`、自动推导或显式配置的 `wal_file` 和 `wal_sync_interval`。WAL 定期同步默认间隔为 60 秒，合并普通缓存追加，减少小块反复写入。核心保留显式正值配置，MSM 升级会将所管理的 `cache_main` 同步间隔更新为产品推荐值。正常关闭、显式保存和批量失效仍强制同步，异常退出可能丢失尚未同步的缓存尾部，这些缓存可由后续 DNS 查询重新获取。

批量失效先同步删除 WAL，再删除内存与 L1 缓存，不再逐批重写全量快照。变更计数仍触发周期 checkpoint，显式保存和退出保存也保持可用。

WAL v2 使用 `mosdns_cache_wal_v2\n` header，后接 16 字节随机 incarnation UUID 和 8 字节大端逻辑 base。逻辑位置只累计 record 字节，不包含 header。`set`、`delete`、`flush` 的 record 编码沿用 v1。gzip 快照的 Name 和 protobuf block 格式保持不变，Extra 的 `MC` 子字段版本 1 记录同一 UUID 和 8 字节大端逻辑 cut。HTTP dump 导入只导入条目，不继承源实例的 UUID 和 cut。

checkpoint 使用两个变更边界。第一个边界内同步旧 WAL，采集不可变快照元数据并记录 cut。gzip、快照文件同步和原子替换在锁外执行，期间的新操作继续追加旧 WAL。第二个边界内同步这些操作，将 cut 之后的完整增量复制到临时 WAL，再同步、原子替换并同步父目录。轮换保留 UUID，将 base 前移到 cut。

恢复只接受以下组合

- 旧快照没有 `MC` 元数据，WAL 为 v1 或 v2 且 base 为零，全量重放。
- 快照具有元数据，WAL 必须为 v2 且 UUID 相同。cut 必须位于 WAL 的逻辑范围内和完整 record 边界上，只重放 cut 之后的增量。

新快照与尚未轮换的旧 WAL 也是合法组合。恢复跳过已由快照覆盖的前缀，避免重复插入触发容量淘汰或复活已失效条目。旧快照与 base 已前移的 WAL、UUID 不匹配、cut 越界或切开 record 都是恢复错误，不会全量回退重放。

首次读取 v1 WAL 时，在缓存开始提供服务前完成恢复，再将相同 records 原样复制为 v2、生成 UUID 并设置 base 为零，文件同步、原子替换后同步父目录。这个阶段的旧快照保持不变。断电时旧快照配 v1 或 v2/base 为零都可恢复。首个成功 checkpoint 才发布绑定 UUID 和 cut 的快照。忽略的未完成 WAL 尾记录在后续追加前通过原子替换移除，完整 records 保留。

恢复错误会清空已部分加载的缓存，后续 DNS 转交上游，停止该实例所有持久化追加和 checkpoint，以保留原文件用于恢复。`last_wal_replay.status` 和日志保留错误，必须恢复匹配文件并重启实例后才能重新使用持久化缓存。

旧二进制不支持 WAL v2。它可能仅记录回放错误后继续运行，丢失快照之后的增量，因此不能只降级二进制，也不能将升级前快照与升级后 WAL 混用。按本手册的成对备份步骤回滚。

## 建议灰度顺序

按独立部署或独立 MosDNS 进程灰度。同一进程内的所有 cache 插件一起升级，不能按 tag 使用不同二进制。保留产品默认的各 tag 持久化设置，默认仅 `cache_main` 开启，不为灰度切换家庭特定的 persist 或 flush 配置。

1. 第一批选择一个独立部署或进程，完成成对备份后升级二进制，其余部署或进程维持原版本。
2. 发布并重启后，观察至少一个 `dump_interval` 周期内的运行态。
3. 确认 WAL 回放、snapshot 保存、命中率和 lazy update 没有异常后，再升级下一批独立部署或进程。

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
  --tag "cache_main" \
  --tag "cache_branch_domestic" \
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

1. 停止实例并保留当前 `.dump`、`.wal` 和错误状态。
2. 检查 UUID、base、cut 和文件路径是否来自同一实例的成对备份。
3. 修复权限或挂载问题，或恢复已知匹配的成对备份。
4. 重启实例，确认 `last_wal_replay.status` 恢复正常且真实 DNS 查询可用。

### 场景 2：周期 dump 失败

现象：

- `last_dump.status = error`

处理：

1. 检查 `cache/` 目录权限、磁盘空间、挂载状态。
2. 确认 snapshot 文件路径未被外部程序占用。
3. 问题未修复前，不要删除现有 `.dump` 文件。
4. 保留持久化配置和 WAL。快照失败不会提前丢弃旧 WAL，修复后再执行显式保存并检查恢复状态。

### 场景 3：UI 显示异常但缓存功能正常

现象：

- DNS 命中正常，但面板为空或字段缺失

处理：

1. 直接访问 `/api/v1/cache/{cache_tag}/stats` 确认接口返回。
2. 若 `/stats` 正常，优先检查前端缓存标签是否与插件 `tag` 一致。
3. 不要回退到解析 `/metrics` 文本的旧方案。

## 回滚步骤

回滚使用升级前的二进制、配置与成对缓存备份。

1. 停止实例，备份当前二进制、配置、`.dump` 和 `.wal`，保留本次运行的增量用于后续恢复。
2. 恢复同一停机备份中的升级前二进制、配置、`.dump` 和 `.wal`，不要混用不同阶段的文件。
3. 重启服务。
4. 用 `/api/v1/cache/{cache_tag}/stats` 确认回放正常，并执行真实 DNS 查询。
5. 保留回滚和升级后的备份直到回滚窗口关闭。

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
