# 变更提案: cache-wal-ui-rollout

## 元信息
```yaml
类型: 优化
方案类型: implementation
优先级: P0
状态: 已完成
创建: 2026-03-06
```

---

## 1. 需求

### 背景
缓存运行时已经完成 `snapshot + WAL + /stats` 重构，但当前生产配置尚未启用 WAL，Web/UI 仍主要依赖 `/metrics` 文本解析，导致新能力无法在默认部署中生效，也无法在界面上展示实例状态、L1/L2 运行态以及最近的 snapshot/WAL 状态。

### 目标
- 在生产配置中为现有缓存实例统一落地 `wal_file` 与 `wal_sync_interval`
- 保持现有 `dump_file`/`dump_interval` 兼容，同时补齐灰度启用和回滚说明
- 将主仪表盘和旧版缓存卡片统一接入 `/plugins/<tag>/stats`
- 在 UI 中展示完整缓存运行态，包括实例状态、L1/L2 容量、关键命中计数、snapshot/WAL 最近状态

### 约束条件
```yaml
时间约束: 当前回合内完成实现和验证
性能约束: 不得引入高频重复拉取 /metrics 的额外开销，缓存面板改为按实例并发拉取 /stats
兼容性约束: 保留 dump 快照配置和现有 flush/show 操作；旧页面的缓存展示行为需要继续可用
业务约束: 不修改缓存插件 tag，不破坏现有 Web/UI 页面入口
```

### 验收标准
- [ ] `config/sub_config/cache.yaml` 中所有缓存实例补齐 `wal_file` 与 `wal_sync_interval`
- [ ] `coremain/www/assets/js/log.js` 的缓存面板改为使用 `/plugins/<tag>/stats`
- [ ] `coremain/www/log.html`/`coremain/www/log.min.html` 的缓存表头与渲染结果匹配
- [ ] `coremain/www/mosdns.html` 与 `coremain/www/mosdnsp.html` 的缓存卡片改为使用 `/stats`，并展示新增运行态字段
- [ ] 相关文档/知识库同步，且本地验证通过

---

## 2. 方案

### 技术方案
- 配置侧直接为每个缓存实例启用独立 WAL 文件，沿用现有 dump 文件作为 checkpoint 基线
- 主仪表盘缓存表格从 Prometheus 文本解析改为逐缓存实例请求 `/plugins/<tag>/stats`，并在前端做字段标准化
- 旧版 `mosdns.html`/`mosdnsp.html` 保留系统指标对 `/metrics` 的读取，仅把缓存卡片部分切换到 `/stats`
- 缓存运行态采用组合展示而不是继续横向扩表，通过“实例状态 / 请求命中 / L1-L2 / snapshot / WAL”聚合字段控制桌面和移动端宽度

### 影响范围
```yaml
涉及模块:
  - config/sub_config/cache.yaml: 启用生产 WAL 配置并内联灰度/回滚说明
  - coremain/www/assets/js/log.js: 主仪表盘缓存面板改造
  - coremain/www/log.html: 主仪表盘缓存表头同步
  - coremain/www/log.min.html: 压缩版主仪表盘表头同步
  - coremain/www/mosdns.html: 旧版缓存卡片改造
  - coremain/www/mosdnsp.html: 旧版缓存卡片改造
  - docs/API_REFERENCE.md: 补充 UI/配置落地说明
预计变更文件: 7
```

### 风险评估
| 风险 | 等级 | 应对 |
|------|------|------|
| 旧页面与新主面板字段不一致 | 中 | 统一在前端定义标准化 stats 映射 |
| 并发拉取多个 `/stats` 带来额外请求 | 中 | 仅在缓存面板刷新时请求，并采用 `Promise.allSettled` |
| WAL 启用后磁盘文件增多 | 中 | 每个实例独立 `.wal` 文件，继续通过 snapshot checkpoint 截断 WAL |
| 压缩产物与源码不一致 | 中 | 同步更新 `log.min.js` 与相关 HTML 入口 |

---

## 3. 技术设计

### 架构设计
```mermaid
flowchart TD
    A[cache.yaml] --> B[缓存插件实例]
    B --> C[snapshot dump_file]
    B --> D[WAL wal_file]
    E[Web/UI] --> F[/plugins/<tag>/stats]
    E --> G[/metrics]
    F --> H[缓存运行态展示]
    G --> I[系统指标展示]
```

### API设计
#### GET /plugins/{cache_tag}/stats
- **请求**: 无
- **响应**: `tag`、`backend_size`、`l1_size`、`updated_keys`、`counters`、`last_dump`、`last_load`、`last_wal_replay`、`snapshot_file`、`wal_file`、`config`

### 数据模型
| 字段 | 类型 | 说明 |
|------|------|------|
| `backend_size` | number | 后端缓存条目数 |
| `l1_size` | number | L1 shard 条目数 |
| `counters.query_total` | number | 请求总数 |
| `counters.hit_total` | number | 总命中数 |
| `counters.l1_hit_total` | number | L1 命中数 |
| `counters.l2_hit_total` | number | L2 命中数 |
| `last_dump` | object | 最近 snapshot 状态 |
| `last_wal_replay` | object | 最近 WAL 回放状态 |

---

## 4. 核心场景

> 执行完成后同步到对应模块文档

### 场景: 启动后缓存状态可观测
**模块**: cache / web-ui
**条件**: 缓存实例已配置 `dump_file` 和 `wal_file`
**行为**: UI 拉取 `/plugins/<tag>/stats` 并展示容量、命中、snapshot/WAL 最近状态
**结果**: 运维无需手工解析 `/metrics` 即可查看缓存运行态

### 场景: WAL 灰度与回滚
**模块**: cache / config
**条件**: 生产部署需要启用 WAL
**行为**: 配置文件提供默认 WAL 参数，并说明如何仅删除 `wal_file` 配置进行回滚
**结果**: 保留 dump 快照能力的同时，可按实例停用 WAL

---

## 5. 技术决策

> 本方案涉及的技术决策，归档后成为决策的唯一完整记录

### cache-wal-ui-rollout#D001: 统一以 `/stats` 作为缓存 UI 的主数据源
**日期**: 2026-03-06
**状态**: ✅采纳
**背景**: `/metrics` 只适合聚合指标展示，无法稳定承载 snapshot/WAL 状态、配置回显和 L1/L2 组合视图。
**选项分析**:
| 选项 | 优点 | 缺点 |
|------|------|------|
| A: 继续解析 `/metrics` | 不需要改现有指标抓取逻辑 | 无法表达运行态对象结构，前端解析脆弱 |
| B: 缓存 UI 切到 `/stats` | 字段稳定，直接承载实例状态和操作结果 | 需要逐实例请求并改造前端 |
**决策**: 选择方案 B
**理由**: `/stats` 已经是缓存子系统的运行态单一出口，继续在 UI 侧解析 `/metrics` 只会制造双重事实来源。系统指标仍保留 `/metrics`，缓存面板只切换缓存相关部分，改动最小且职责清晰。
**影响**: 影响 `coremain/www/assets/js/log.js`、`coremain/www/mosdns.html`、`coremain/www/mosdnsp.html` 的缓存数据获取逻辑

---
