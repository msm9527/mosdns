# 分流记忆系统重构与优化方案

## 1. 文档定位

本文档面向当前项目中的“分流缓存 / 刷新分流缓存”功能，给出一份完整的工程化重构方案。

为了避免术语混乱，本文档统一使用以下表述：

- `DNS 响应缓存`：指 `type: cache` 插件缓存 DNS 应答结果
- `分流记忆系统`：指域名在运行中被学习、刷新、发布为分流规则的整套机制
- `刷新分流记忆`：指 `requery` 对已学习域名进行重新校验和重建

本文档聚焦的是第二类和第三类能力，而不是 `cache` 插件本体。

## 2. 范围与边界

### 2.1 纳入范围

- `plugin/executable/domain_output`
- `plugin/executable/requery`
- `config/sub_config/domain_output.yaml`
- `config/sub_config/requery.yaml`
- `config/sub_config/process_v4.yaml`
- `config/sub_config/process_v6.yaml`
- `config/sub_config/not_in_list_ipmatch.yaml`
- `coremain/www/assets/js/log.js` 中分流缓存相关交互

### 2.2 不直接纳入范围

- `plugin/executable/cache` 的 DNS 响应缓存实现
- `pkg/cache` 的底层通用缓存容器
- UI 的缓存 `/stats` 面板本身

但本文档会明确讨论分流记忆系统和 `cache_*` 之间的联动边界。

## 3. 当前实现基线

### 3.1 主流程中的“观察与写入”

当前主流程会在查询过程中把域名写入多类记忆列表，例如：

- `my_realiplist`
- `my_fakeiplist`
- `my_nov4list`
- `my_nov6list`

对应配置入口主要位于：

- `config/sub_config/process_v4.yaml`
- `config/sub_config/process_v6.yaml`
- `config/sub_config/not_in_list_ipmatch.yaml`

写入触发条件依赖：

- `resp_ip`
- `rcode`
- `fast_mark`
- `switch9`
- `switch3`
- `switch4`

### 3.2 `domain_output` 的职责

`plugin/executable/domain_output/domain_output.go` 当前同时承担：

- 运行时观察采集
- 内存聚合统计
- 周期落盘
- 规则文件生成
- 规则回灌到 `domain_set_url`

典型输出文件包括：

- `gen/realiplist.txt`
- `gen/fakeiplist.txt`
- `gen/nov4list.txt`
- `gen/nov6list.txt`
- 对应 `gen/*rule.txt`

### 3.3 `requery` 的职责

`plugin/executable/requery/requery.go` 当前流程为：

1. `save_rules`
2. 合并 source files 中的域名
3. `flush_rules`
4. 通过 `ResolverAddress` 重发 A/AAAA 查询
5. 再次 `save_rules`

### 3.4 与 DNS 响应缓存的关系

当前 `requery` 并不是一个独立的“旁路验证器”，而是通过 `ResolverAddress` 命中一条真实的查询入口。

在当前配置中，这条入口仍会经过：

- `cache_all / cache_all_noleak`
- `cache_cn`
- `cache_google`
- `cache_google_node`
- `cache_cnmihomo`

因此当前的“刷新分流缓存”可能受到已有 DNS 响应缓存影响。

## 4. 当前实现的核心问题

## 4.1 单次观察直接晋升，误报成本低

当前大量分流记忆写入是“单次观察直接入库”的方式，存在以下问题：

- 一次污染结果即可写入长期规则
- 一次假阴性即可把域名打成 `nov4 / nov6`
- fakeip / realip 之间缺少竞争机制

这会导致误报和错误缓存直接被放大为长期规则。

## 4.2 缺少证据层和置信度模型

当前 `domain_output` 只记录：

- `Count`
- `LastDate`

但没有：

- success / failure 计数
- 最近验证时间
- 来源路径
- 冲突证据
- 置信分数
- 生效状态

这意味着系统无法严肃判断“这个域名是否已被可靠验证”。

## 4.3 `domain_output` 职责耦合过重

当前 `domain_output` 同时承担：

- 采样器
- 聚合器
- 持久化器
- 规则编译器
- 规则发布器

这会造成：

- 性能问题难定位
- 功能难扩展
- 失败难隔离
- 规则状态不可解释

## 4.4 全量写、全量推送带来高放大成本

当前 `performWrite()` 会：

- 重写整个 `file_stat`
- 重写整个 `file_rule`
- 重写整个 `gen_rule`
- 再把所有域名整包推送到 `domain_set_url`

副作用：

- IO 放大
- CPU 放大
- 错误规则的影响范围被扩大
- 大列表时刷新和保存成本持续上升

## 4.5 观察粒度和生效粒度不一致

当前 `domain_output` 内部支持 AD/CD/DO flags 进入 key，但生成规则时会丢弃 flags，只保留裸域名。

这意味着：

- 内部观察是带上下文的
- 外部规则却是全局无上下文的

结果是不同查询条件下的观察被粗暴合并成一条规则。

## 4.6 刷新路径本身不干净

当前 `requery` 刷新过程可能命中 `cache_*`，导致“刷新”并不总是重新向真实上游校验，而可能只是命中了旧响应缓存。

这会造成：

- 旧偏差被重复强化
- 规则被旧缓存污染
- 实时性下降
- 准确性下降

## 4.7 刷新是“全量双发”，噪声大

当前 `requery` 对每个域名固定发：

- A
- AAAA

这会带来：

- 对单栈域名产生无效噪声
- 增加误报 `nov4 / nov6`
- 增加上游压力
- 降低刷新有效样本密度

## 4.8 刷新过程先清后建，失败窗口大

当前流程在重建前就执行 `flush_rules`。如果中途失败：

- 旧规则已经被清
- 新规则尚未完成
- 线上系统会落入不完整状态

这会直接损害实时性和准确性。

## 4.9 缺少观察丢弃指标

`domain_output` 的采样通道满了之后直接丢弃，但没有观测指标。

这意味着：

- 高峰期学习样本可能严重丢失
- 但系统外部无法判断当前分流记忆是否仍然可信

## 5. 设计目标

### 5.1 核心目标

- 降低误报率
- 降低错误分流记忆的滞留时间
- 提高分流记忆命中率和有效性
- 提高刷新效率
- 在保证实时性的同时提高准确性
- 让系统可解释、可验证、可回滚

### 5.2 具体工程目标

- 将“观察”与“最终规则生效”解耦
- 将“刷新分流记忆”改为真正的旁路验证
- 引入晋升阈值、衰减和冲突裁决
- 从全量重建改为增量或原子切换
- 提供清晰的运行态观测接口

### 5.3 非目标

- 不在第一阶段重构所有主链路 YAML 结构
- 不在第一阶段替换 DNS 响应缓存实现
- 不在没有证据模型前提前做复杂机器学习判定

## 6. 设计原则

- 观察不是判决
- 刷新必须旁路 DNS 响应缓存
- 规则必须从证据中晋升，而不是从单次执行路径直接产生
- 刷新过程不允许破坏现有可用规则集
- 热路径优先保护延迟，学习链路用异步方式承载
- 所有可疑结论都必须具备回退机制

## 7. 目标架构

建议把当前系统拆成四层：

```text
查询路径
  -> 观察层（Observation Layer）
     -> 证据存储层（Evidence Store）
        -> 判定层（Decision Engine）
           -> 生效层（Activation Layer）

刷新路径
  -> 刷新编排器（Refresh Orchestrator）
     -> 旁路验证器（Refresh Resolver）
     -> 证据更新
     -> 规则原子切换
```

### 7.1 Observation Layer

职责：

- 只记录运行中观察到的事实
- 生成标准化 evidence event
- 不直接改动最终生效规则

建议采集字段：

- domain
- qtype
- decision_kind
- flags
- source_path
- observed_at
- resolver_mode
- result_summary

### 7.2 Evidence Store

职责：

- 保存域名粒度的历史证据
- 支持聚合、冲突分析、衰减、晋升判断

建议状态：

- `observed`
- `candidate`
- `promoted`
- `decaying`
- `evicted`

### 7.3 Decision Engine

职责：

- 依据证据决定域名是否应进入：
  - `realip`
  - `fakeip`
  - `nov4`
  - `nov6`

必须支持：

- 晋升阈值
- 时间衰减
- 正反证据竞争
- 按 qtype 区分
- 旁路验证权重更高

### 7.4 Activation Layer

职责：

- 从证据生成运行规则
- 使用 `staging -> active` 原子切换生效
- 避免“先清空再重建”的危险窗口

## 8. 建议的数据模型

建议新增统一的域名证据结构：

```text
domain
qtype_mask
source_mask
flags_mask
state
confidence_score
success_realip_count
success_fakeip_count
success_nov4_count
success_nov6_count
failure_count
last_seen_at
last_verified_at
last_promoted_at
last_conflict_at
version
```

### 8.1 为什么需要这个模型

因为当前只靠 `Count + LastDate` 无法表达：

- 多次验证后的稳定性
- 与对立证据的竞争关系
- 规则何时应该降级或淘汰
- 最新证据是否比旧证据更可信

## 9. 晋升与淘汰策略建议

### 9.1 `realip` 晋升规则

建议至少满足以下条件之一才晋升：

- 最近 3 次观测中至少 2 次为 `realip`
- 最近一次旁路验证成功
- 最近 N 天没有更强 `fakeip` 冲突证据

### 9.2 `fakeip` 晋升规则

- 最近 3 次中至少 2 次在 fakeip 路径成功
- 最近一次旁路验证仍支持 fakeip
- 如果近期出现稳定 realip 证据，则降级为 `candidate`

### 9.3 `nov4 / nov6` 晋升规则

- 不允许单次直接晋升
- 至少需要 2~3 次同类结果
- 必须按 qtype 分开记录
- 最近一次验证恢复正常时应立即降级

### 9.4 衰减规则

- 长期未命中的域名逐步降低置信分数
- 长期未验证的 `promoted` 规则自动降为 `decaying`
- 被对立证据反复击穿后自动淘汰

## 10. 刷新系统重构建议

## 10.1 把 `requery` 改造成刷新编排器

新的 `requery` 不应只做“重发查询”，而应负责：

- 选择刷新目标
- 控制刷新优先级
- 调度旁路验证
- 汇总验证结果
- 协调规则切换

## 10.2 引入旁路刷新路径

建议新增一条专用 refresh pipeline：

```text
refresh_resolver
  -> minimal guard
  -> no cache
  -> actual upstream validation
```

约束：

- 不经过 `cache_all`
- 不经过 `cache_cn`
- 不经过 `cache_google`
- 不经过 `cache_google_node`
- 不经过 `cache_cnmihomo`

这样才能保证“刷新”是对真实上游的再次验证，而不是对旧缓存的重放。

## 10.3 由全量刷新改为优先级刷新

建议把域名按风险和价值分层：

- 高优先级
  - 高频访问
  - 冲突证据多
  - 即将过期
  - 最近验证失败
- 中优先级
  - 普通候选域名
  - 长时间未验证
- 低优先级
  - 长期稳定低频域名

刷新优先级应优先保障：

- 冲突域名
- 热门域名
- 最近规则变化域名

## 10.4 按 qtype 定向刷新

建议记录：

- `seen_a`
- `seen_aaaa`
- `last_success_qtype`
- `qtype_confidence`

刷新时：

- 只见过 A 的域名默认只发 A
- 只见过 AAAA 的域名默认只发 AAAA
- 双栈高频域名才双发

这样可以减少：

- 假 `nov4`
- 假 `nov6`
- 上游压力
- 刷新时的无效噪声

## 11. `domain_output` 重构建议

## 11.1 从“最终规则写入器”退化为“观察事件采集器”

建议把 `domain_output` 的职责收缩为：

- 采集
- 基本聚合
- 落证据

不再直接承担最终规则晋升逻辑。

## 11.2 从全量写改为增量写

建议拆成：

- append-only evidence log
- compacted aggregate snapshot
- diff-based rule publish

优势：

- 写放大降低
- 推送放大降低
- 大规模数据下可持续运行

## 11.3 暴露采样丢弃指标

新增指标建议：

- `route_memory_observation_total`
- `route_memory_observation_dropped_total`
- `route_memory_queue_size`
- `route_memory_flush_total`
- `route_memory_publish_total`

这能让系统在高峰期是否“学坏”变得可见。

## 12. 生效层重构建议

## 12.1 双缓冲规则集

引入：

- `active rule set`
- `staging rule set`

流程：

1. 根据证据生成 staging
2. 对 staging 做完整校验
3. 原子切换 active -> staging
4. 如果失败，保持旧 active 不变

## 12.2 不再允许先 flush 再慢慢重建

原因：

- 这会产生错误窗口
- 中途中断会伤害线上准确性

重构后目标是：

- 构建和发布分离
- 发布是原子动作

## 13. 可观测性设计

建议新增独立运行态接口，例如：

- `/plugins/route_memory/stats`
- `/plugins/route_memory/domain/{domain}`

建议观测项：

- `evidence_total`
- `candidate_total`
- `promoted_total`
- `decaying_total`
- `evicted_total`
- `refresh_total`
- `refresh_verified_total`
- `refresh_conflict_total`
- `observation_dropped_total`
- `publish_duration`

并且支持单域名解释：

- 当前状态
- 最近证据
- 置信分数
- 冲突记录
- 最近刷新时间
- 当前为什么归入 `realip / fakeip / nov4 / nov6`

## 14. 推荐模块划分

未来建议拆成独立模块：

```text
plugin/executable/route_memory/
├── observer.go
├── evidence_store.go
├── scorer.go
├── promoter.go
├── refresh_orchestrator.go
├── refresh_resolver.go
├── rule_compiler.go
├── publisher.go
├── stats.go
└── api.go
```

避免继续把不同职责压在：

- `domain_output`
- `requery`
- 若干 YAML 侧写入动作

## 15. 分阶段实施计划

## Phase 1：止血与校验路径纠偏

目标：

- 降低误报
- 防止旧缓存自我强化

改动建议：

- `requery` 引入 no-cache refresh path
- `nov4 / nov6` 至少二次确认
- 增加 observation dropped metrics
- 给 `realip / fakeip` 增加最小晋升阈值

验收：

- 刷新路径不再命中 `cache_*`
- `nov4 / nov6` 误报率下降
- 刷新任务时长可量化

## Phase 2：引入证据模型

目标：

- 把观察与生效规则解耦

改动建议：

- 新增 evidence store
- 增加 candidate/promoted/decaying 生命周期
- 增加冲突裁决和时间衰减

验收：

- 单次观测不再直接晋升
- 对立证据可解释
- 错误分流记忆能自动退化

## Phase 3：增量发布与原子切换

目标：

- 提高效率
- 提升刷新时的一致性

改动建议：

- 增量规则发布
- `active/staging` 双缓冲
- 刷新失败不破坏现有规则

验收：

- 刷新中断时线上规则不退化
- 发布耗时显著下降
- 大规模规则集下系统稳定

## 16. 风险与回滚策略

### 风险

- 旁路刷新路径与现有生产路径不一致，可能引入判定偏差
- 引入证据模型后，短期内规则命中率可能下降
- 双缓冲发布需要处理旧规则集和新规则集一致性

### 回滚策略

- 每个阶段都保留现有 `domain_output + requery` 兼容路径
- 新能力通过独立开关启用
- `active/staging` 机制下发布失败直接回退到旧 active
- 刷新旁路可按实例或按规则集灰度

## 17. 最终建议

如果后续继续推进，建议不再把这个系统叫“分流缓存优化”，而应正式按“分流记忆系统重构”推进。

推荐实施顺序：

1. `requery` 旁路刷新
2. 晋升阈值与衰减机制
3. qtype 定向刷新
4. 证据模型
5. 原子规则切换
6. 增量发布

原因：

- 前三项直接影响误报、错误缓存、准确性
- 后三项直接影响效率、稳定性、可维护性

## 18. 结论

当前系统的主要问题不是“规则数量不够”，而是：

- 证据不足就晋升
- 刷新路径不干净
- 规则发布不是原子的
- 观察、判决、发布耦合过深

因此，专业的重构方向应当是：

- 观察与判决分离
- 刷新与缓存解耦
- 规则发布原子化
- 规则晋升证据化

只有这样，才能同时满足：

- 更高效率
- 更低误报
- 更少错误缓存
- 更高缓存利用率
- 更好的实时性和准确性
