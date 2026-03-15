# 分流记忆混合模式说明

## 1. 文档目的

本文档说明当前“刷新分流缓存”的混合模式工作方式。

这里统一使用以下表述：

- `分流记忆`：运行过程中学习得到的 `realip / fakeip / nov4 / nov6` 域名记忆
- `按需刷新`：域名再次被访问时，由系统自动判断是否需要立即刷新
- `低频巡检`：系统按较长时间间隔做一次兜底刷新

本文档不讨论 `type: cache` 的 DNS 响应缓存，只讨论分流记忆的刷新机制。

## 2. 为什么要用混合模式

旧方式更依赖：

- 手动点一次“开始全新任务”
- 或者设置一个定时任务定期全量刷新

这有两个问题：

- 太依赖人工或 cron
- 全量刷新成本高，但真正需要刷新的域名通常只是一小部分

混合模式的目标是：

- 让热点域名、异常域名、冲突域名优先自动刷新
- 让低频巡检只负责兜底
- 降低无效刷新量
- 维持实时性和准确性

## 3. 三种模式

`requery` 现在支持三种模式：

### 3.1 `hybrid`

推荐默认值。

行为：

- 查询链路中发现“需要纠偏”的域名时，自动进入按需刷新队列
- 同时保留低频巡检

适合：

- 正常生产环境
- 希望系统自动纠偏，但又不想完全放弃巡检兜底

### 3.2 `manual`

行为：

- 不启用低频巡检
- 仍允许按需刷新
- 手动触发“开始全新任务”仍可执行

适合：

- 流量小
- 更希望减少后台刷新动作
- 自己对分流行为有较强控制要求

### 3.3 `scheduled`

行为：

- 只保留低频巡检
- 关闭按需刷新队列

适合：

- 需要完全稳定、可预测的后台刷新节奏
- 不希望查询链路主动触发刷新

不推荐作为默认模式。

## 4. 混合模式的两条刷新路径

## 4.1 按需刷新

按需刷新是主机制。

它的特点是：

- 由实际访问流量触发
- 只刷新真正需要刷新的域名
- 异步执行，不阻塞当前请求
- 默认走旁路 `refresh_resolver_address`

按需刷新不会每次访问都触发。
它会先判断：

- 条目是否已经晋升为有效分流记忆
- 是否超过陈旧窗口
- 是否仍在冷却窗口内

满足条件时，`domain_output` 会向 `requery` 的 `/enqueue` 上报一个刷新任务。

## 4.2 低频巡检

低频巡检是兜底机制。

它的特点是：

- 周期长
- 负责补漏
- 负责处理长期未访问但仍保留在分流记忆中的域名

推荐做法：

- 每 12 小时或 24 小时一次

不建议继续使用高频 cron 做主机制。

## 5. 按需刷新什么时候触发

当前实现里，按需刷新主要依赖分流记忆条目的陈旧判断和冷却判断。

典型触发条件：

- 域名已是已发布分流记忆
- 再次被访问
- 超过 `stale_after_minutes`
- 不在 `refresh_cooldown_minutes` 冷却窗口中

满足这些条件后：

- 条目会被标记为 `dirty`
- 记下 `dirty_reason`
- 异步进入 `requery` 队列

刷新完成后：

- `requery` 会调用对应列表实例的 `/verify`
- 条目状态回到 `clean`

## 6. 低频巡检什么时候执行

低频巡检由 `scheduler` 控制：

- `enabled`
- `interval_minutes`

在混合模式里，它不是主要刷新来源，而是保底巡检。

如果你已经启用 `hybrid`，建议：

- 打开巡检
- 但把频率降下来

例如：

- `1440` 分钟，即每天一次

## 7. 关键配置项

## 7.1 `requeryconfig.json`

示例：

```json
{
  "workflow": {
    "mode": "hybrid",
    "flush_mode": "none",
    "save_before_refresh": true,
    "save_after_refresh": true
  },
  "scheduler": {
    "enabled": true,
    "start_datetime": "",
    "interval_minutes": 1440
  },
  "execution_settings": {
    "refresh_resolver_address": "127.0.0.1:7767",
    "query_mode": "observed",
    "date_range_days": 30,
    "max_queue_size": 2048
  }
}
```

关键字段说明：

- `workflow.mode`
  - `hybrid / manual / scheduled`
- `scheduler.enabled`
  - 是否启用低频巡检
- `scheduler.interval_minutes`
  - 巡检间隔
- `execution_settings.refresh_resolver_address`
  - 旁路刷新专用 resolver
- `execution_settings.query_mode`
  - `observed` 表示按历史观察到的 `A/AAAA` 类型定向刷新
- `execution_settings.date_range_days`
  - 仅刷新最近 N 天出现过的域名
- `execution_settings.max_queue_size`
  - 按需刷新队列上限

## 7.2 `domain_output.yaml`

每类分流记忆条目还需要配置自己的刷新策略。

常见字段：

- `stale_after_minutes`
  - 多久后认为条目需要重新验证
- `refresh_cooldown_minutes`
  - 同一域名刷新后，多久内不重复进入队列
- `on_dirty_url`
  - 脏条目上报地址，通常指向 `/api/v1/runtime/requery/enqueue`
- `verify_url`
  - 刷新完成后回写验证状态的地址

示例：

```yaml
policy:
  kind: realip
  promote_after: 2
  decay_days: 21
  track_qtype: true
  publish_mode: promoted_only
  stale_after_minutes: 360
  refresh_cooldown_minutes: 120
  on_dirty_url: "http://127.0.0.1:9099/api/v1/runtime/requery/enqueue"
  verify_url: "http://127.0.0.1:9099/api/v1/memory/my_realiplist/verify"
```

## 8. 推荐默认值

建议先从下面这组开始：

- 模式：`hybrid`
- 低频巡检：开启
- 巡检间隔：`1440` 分钟
- `date_range_days`：`30`
- `max_queue_size`：`2048`

列表级策略建议：

- `realip`
  - `stale_after_minutes: 360`
  - `refresh_cooldown_minutes: 120`
- `fakeip`
  - `stale_after_minutes: 240`
  - `refresh_cooldown_minutes: 120`
- `nov4 / nov6`
  - `stale_after_minutes: 180`
  - `refresh_cooldown_minutes: 120`

## 9. 运行时怎么看是否正常

### 9.1 看 `requery` 状态

可通过 UI 或接口观察：

- `pending_queue`
- `on_demand_triggered`
- `on_demand_processed`
- `on_demand_skipped`
- `last_on_demand_at`
- `last_on_demand_domain`

如果：

- `pending_queue` 长期堆积
- `on_demand_skipped` 增长过快

通常说明：

- 队列太小
- 触发太频繁
- 冷却窗口太短

### 9.2 看分流记忆列表状态

每个 `domain_output` 实例的 `/stats` 可看到：

- `total_entries`
- `dirty_entries`
- `promoted_entries`
- `published_rules`

如果 `dirty_entries` 长期不下降，说明：

- 按需刷新在入队
- 但刷新闭环没有跑完
- 需要检查 `requery` 和 `verify_url`

## 10. 什么时候该手动点“开始全新任务”

混合模式下，手动全量刷新仍然有意义，但不应该变成日常依赖。

适合手动触发的场景：

- 刚改了大批规则
- 刚切换了核心模式
- 刚切换了 fakeip / 上游策略
- 想做一次完整巡检

不适合把它当成：

- 每天手动维护动作
- 用来替代正常按需刷新

## 11. 什么时候该用哪种模式

建议：

- 正常生产环境：`hybrid`
- 特别保守、希望最少后台动作：`manual`
- 需要完全固定巡检节奏：`scheduled`

如果你不确定：

- 直接选 `hybrid`

## 12. 一句话总结

混合模式的核心不是“多一种定时任务写法”，而是：

- 让真正有问题、需要纠偏的域名优先自动刷新
- 让低频巡检只负责补漏
- 把“刷新分流记忆”从人工维护动作，变成默认可持续运行的机制
