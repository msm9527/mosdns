# cache 模块

## 涉及路径

- `plugin/executable/cache`
- `plugin/executable/aliapi`
- `pkg/cache`
- `config/sub_config/cache.yaml`
- `config/sub_config/forward_local.yaml`
- `config/sub_config/forward_nocn.yaml`
- `coremain/www/assets/js/log.js`
- `coremain/www/mosdns.html`
- `coremain/www/mosdnsp.html`
- `docs/CACHE_WAL_ROLLOUT.md`
- `scripts/check_cache_stats.py`

## 职责

### `plugin/executable/cache`

- 处理 DNS 查询缓存命中与回源
- 维护 L1 热点缓存与 L2 主缓存
- 提供 snapshot + WAL 持久化恢复
- 暴露 `/flush`、`/dump`、`/save`、`/load_dump`、`/show`、`/stats` API
- 暴露 Prometheus 指标，包括查询、命中、lazy update、snapshot、WAL 相关指标

### `pkg/cache`

- 提供通用并发安全缓存容器
- 支持 `Get`、`Store`、`Range`、`Delete`、`Flush`、`Len`、`Close`

### `config/sub_config/cache.yaml`

- 为各缓存实例定义 `dump_file`、`dump_interval`、`wal_file`、`wal_sync_interval`
- 支持 `nxdomain_ttl`、`servfail_ttl`，将正常负结果与短期失败缓存分开治理
- 生产启用时默认保留 snapshot + WAL 双持久化链路
- 回滚到仅 snapshot 模式时只需移除 `wal_file` / `wal_sync_interval`

### `plugin/executable/aliapi`

- 负责并发上游抢答、坏上游熔断和 transport error 的短期失败抑制
- 支持 `failure_suppress_ttl`、`upstream_failure_threshold`、`upstream_circuit_break_seconds`
- 当所有选中上游都 transport fail 时，返回短期 `SERVFAIL`，降低热点失败域名对上游的重复冲击

### `docs/CACHE_WAL_ROLLOUT.md`

- 提供 WAL 灰度启用、运行态观测和回滚手册
- 约定统一通过 `/plugins/{cache_tag}/stats` 做发布后检查

### `scripts/check_cache_stats.py`

- 提供零依赖的 cache `/stats` 巡检脚本
- 支持从 `config/sub_config/cache.yaml` 自动提取 cache tag
- 支持 `--require-wal` 与 `--strict` 作为发布后验收开关

### Web/UI

- `log.js` 的缓存面板通过 `/plugins/<tag>/stats` 展示运行态
- `mosdns.html` / `mosdnsp.html` 的缓存卡片也通过 `/stats` 展示实例状态、L1/L2、snapshot/WAL 状态
- 系统级资源指标仍通过 `/metrics` 获取，不与缓存运行态面板混用

## 关键实现说明

- snapshot 采用临时文件写入、`fsync`、原子 `rename` 的落盘方式
- WAL 为可选能力，通过 `wal_file` 和 `wal_sync_interval` 控制
- 启动恢复顺序为：snapshot -> WAL replay
- 缓存条目解包失败时会主动删除损坏项，避免坏数据长期留存
- 缓存 UI 不再解析 Prometheus 文本来拼装缓存状态，`/stats` 是缓存运行态的单一事实来源
- 当前失败治理默认值：`NXDOMAIN=60s`、`SERVFAIL=15s`、`transport suppress=10s`、`circuit threshold=3`、`circuit break=60s`
