# shunt_memory 模块

## 作用

- 负责分流记忆采集、晋升、衰减和规则发布
- 负责刷新分流记忆时的旁路重查
- 与 `cache_*` 的 DNS 响应缓存协同，但不再依赖旧缓存做刷新验证

## 关键实现

- `plugin/executable/domain_output/domain_output.go`
  - 引入 `policy` 配置块
  - 观察结果先进入聚合状态，再按阈值晋升为规则
  - `file_stat` 扩展为带 `qmask/score/promoted/dirty` 的兼容格式
  - live 查询命中后可按 `stale_after_minutes/refresh_cooldown_minutes` 标记脏状态并异步上报 `requery`
  - 规则文件与 `domain_set_url` 改为按晋升结果原子发布
- `plugin/executable/domain_output/api.go`
  - 保留 `/save` `/flush` `/show`
  - 新增 `/stats` 和 `/verify`
- `plugin/executable/requery/requery.go`
  - 新增 `workflow.mode`，支持 `hybrid/manual/scheduled`
  - 默认采用“按需刷新 + 低频巡检”
  - 刷新查询按已观察 `qmask` 定向发送 A/AAAA
  - 新增 `/enqueue` 按需刷新队列
- `config/sub_config/requery_refresh.yaml`
  - 新增 refresh 专用旁路链路，绕过 `cache_all/cache_cn/cache_google/...`

## 默认配置口径

- `domain_output` 推荐使用 `policy.kind/promote_after/decay_days/track_qtype/publish_mode`
- 若需要混合模式，补充：
  - `policy.stale_after_minutes`
  - `policy.refresh_cooldown_minutes`
  - `policy.on_dirty_url`
  - `policy.verify_url`
- `requery` 推荐使用：
  - `workflow.mode: hybrid`
  - `workflow.flush_mode: none`
  - `workflow.save_before_refresh: true`
  - `workflow.save_after_refresh: true`
  - `execution_settings.refresh_resolver_address: 127.0.0.1:7767`
  - `execution_settings.query_mode: observed`
  - `execution_settings.max_queue_size: 2048`

## 兼容性

- 旧 `domain_output` 文本统计文件仍可加载
- 旧 `requery` 配置若仍带 `flush_rules` 且未声明 `workflow.flush_mode`，默认按 `legacy` 行为兼容
- 旧 `requery` 配置若未声明 `workflow.mode`，默认按 `scheduler.enabled` 映射为 `hybrid/manual`
- UI 与脚本仍可继续使用现有 `requery` API

## UI 接入

- `coremain/www/assets/js/log.js`
  - `requery` 面板改为以“刷新模式 + 低频巡检 + 按需队列”展示混合模式
- `coremain/www/log.html`
  - 维持简洁面板，只展示必要统计表
