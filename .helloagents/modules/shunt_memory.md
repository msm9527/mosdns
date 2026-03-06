# shunt_memory 模块

## 作用

- 负责分流记忆采集、晋升、衰减和规则发布
- 负责刷新分流记忆时的旁路重查
- 与 `cache_*` 的 DNS 响应缓存协同，但不再依赖旧缓存做刷新验证

## 关键实现

- `plugin/executable/domain_output/domain_output.go`
  - 引入 `policy` 配置块
  - 观察结果先进入聚合状态，再按阈值晋升为规则
  - `file_stat` 扩展为带 `qmask/score/promoted` 的兼容格式
  - 规则文件与 `domain_set_url` 改为按晋升结果原子发布
- `plugin/executable/domain_output/api.go`
  - 保留 `/save` `/flush` `/show`
  - 新增 `/stats`，可查看晋升策略和 dropped observations
- `plugin/executable/requery/requery.go`
  - 新增 `workflow` 与 `refresh_resolver_address/query_mode`
  - 默认采用 staged 刷新，不再默认执行 legacy flush
  - 刷新查询按已观察 `qmask` 定向发送 A/AAAA
- `config/sub_config/requery_refresh.yaml`
  - 新增 refresh 专用旁路链路，绕过 `cache_all/cache_cn/cache_google/...`

## 默认配置口径

- `domain_output` 推荐使用 `policy.kind/promote_after/decay_days/track_qtype/publish_mode`
- `requery` 推荐使用：
  - `workflow.flush_mode: none`
  - `workflow.save_before_refresh: true`
  - `workflow.save_after_refresh: true`
  - `execution_settings.refresh_resolver_address: 127.0.0.1:7767`
  - `execution_settings.query_mode: observed`

## 兼容性

- 旧 `domain_output` 文本统计文件仍可加载
- 旧 `requery` 配置若仍带 `flush_rules` 且未声明 `workflow.flush_mode`，默认按 `legacy` 行为兼容
- UI 与脚本仍可继续使用现有 `requery` API

## UI 接入

- `coremain/www/assets/js/log.js`
  - `requery` 面板改为直接读取 `my_fakeiplist/my_realiplist/my_nov4list/my_nov6list` 的 `/stats`
  - 当前 UI 仅保留状态、数量和操作，不展示策略说明型文案
- `coremain/www/log.html`
  - 维持简洁面板，只展示必要统计表
