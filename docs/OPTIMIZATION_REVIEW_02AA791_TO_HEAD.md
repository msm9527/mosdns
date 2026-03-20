# 02aa791 到 HEAD 的深度优化回顾

## 一页结论

从 `02aa7918967b052c83a6df5387f7ed573263c137` 到当前 `HEAD`，这段区间不是零散修补，而是一轮连续的系统性收口。

- 区间提交数：`179`
- 变更规模：`475 files changed, 58715 insertions, 255689 deletions`
- 结果形态：mosdns 从“运行态分散、接口割裂、规则和配置耦合、数据面局部补丁化”的状态，推进到了“配置面、控制面、数据面边界清晰，状态可恢复，行为可观测，接口可稳定演进”的状态。

最重要的 5 个结论如下：

1. 运行态统一收口到 `control.db` 和 `/api/v1/control/*`，状态真源从散文件变为统一控制面。
2. 数据面热点路径被重新梳理，`aliapi`、`forward`、`cache`、`domain_mapper`、`sequence` 不再停留在局部 patch。
3. 故障治理升级成“有状态、有抑制、有恢复”的体系，包含 upstream 熔断、SERVFAIL 热点抑制、cache WAL、requery 自动恢复。
4. 规则源、配置目录、自定义配置、内置 UI 与 OpenAPI 被统一到 V2 口径，系统开始具备可持续演进空间。
5. 审计、统计、健康检查、系统事件和前端面板形成闭环，控制面真正具备可观测和可运维性。

一句话概括：

> 这段优化的本质，不是把几个查询再跑快一点，而是把整个系统推进到“可持久化、可恢复、可解释、可运维、可继续演进”的状态。

## 分析范围与方法

- 起点提交：`02aa7918967b052c83a6df5387f7ed573263c137`
- 起点主题：`fix(udp_server): harden fast bypass and observability / 强化快路径安全与可观测性`
- 终点：`HEAD`
- 主要证据：
  - 179 个 commit 的主题演进
  - 目录热区：`coremain/`、`plugin/executable/*`、`config/`、`docs/`
  - 关键实现入口：`runtime_state_store`、`cache/persistence`、`requery_recovery`、`forward/exchange`、`audit_collector`、`configv2/compiler`
  - 现有设计文档：V2 架构、cache WAL、DNS failure flow、shunt memory hybrid mode

需要额外说明的一点是：`255689` 行删除并不表示能力缩水。大量删除来自旧 UI 快照、旧兼容层、大体积生成规则文件和旧配置产物的主动收口；这类“删掉旧真源和旧表述”的动作，本身就是这轮深度优化的重要组成部分。

## 总体判断

按主题归并，这段区间主要完成了 7 条主线优化：

| 主线 | 优化结果 | 本质变化 |
| --- | --- | --- |
| 运行态与控制面 | `control.db` + `/api/v1/control/*` | 状态真源统一 |
| 数据面热路径 | `aliapi` / `forward` / `domain_mapper` / `sequence` | 减少无意义工作 |
| 缓存系统 | snapshot + WAL + stats API + UI | 从内存缓存升级为可恢复缓存 |
| requery / domain pool / memory pool | checkpoint、自动恢复、热规则闭环 | 从离线动作升级为后台工作流 |
| 规则源与 config v2 | compiler、custom_config、rule source service | 从历史堆叠升级为明确编译模型 |
| 审计与可观测性 | SQLite audit、search、overview、实时聚合 | 从“有日志”升级为“有闭环观测” |
| 运维与稳定性 | health、自检、event audit、restart/update、stress tool | 从人工处理升级为体系化运维 |

下面按这 7 条主线展开。

## 1. 运行态统一收口：从散文件状态走向 `control.db`

这一段最深的一层优化，是把原本散落在多个 JSON/TXT/局部状态文件中的运行态，逐步收口为：

- `config v2` 作为唯一配置入口
- `control.db` 作为主要运行态真源
- `/api/v1/control/*` 作为稳定控制面

这不是换个文件格式，而是重新划定系统边界。旧模型的问题主要有 3 个：配置和运行态混在一起，谁是真源不清楚；各模块各自落盘，状态分散，无法统一做健康检查和统一导出；前端、脚本、插件内部实现互相耦合，接口不稳定，系统很难继续演进。收口之后，配置面负责声明，控制面负责状态，数据面负责执行，后续的审计、状态恢复、健康检查、CLI 控制和 UI 面板都建立在这个分层之上。

这条线的实现集中在 `coremain/runtime_state_store.go`、`internal/store/sqlite/runtime.go`、`coremain/api_runtime.go`、`coremain/control_health.go`、`coremain/runtime_cmd.go`。具体机制包括：引入统一 runtime state store；用 namespace 管理 `webinfo`、`requery`、`adguard_rule`、`diversion_rule` 等状态；按 base dir 隔离 `control.db` 路径，避免不同实例状态串扰；将旧 `runtime` 语义整体收口为 `control`；提供 `summary`、`health`、`events`、`upstreams` 等稳定控制面接口。

代表性提交：

- `8eb15d1` 为 overrides 与 upstream 接入 SQLite 状态存储
- `fa51764` 为 switch 与 webinfo 接入 SQLite 状态存储
- `67fe836` 为 requery 配置与状态接入 SQLite 状态存储
- `9f1959d` 按状态文件目录隔离 SQLite 存储路径
- `d92e9ec` 增加统一 runtime 资源接口
- `0d1173a` 将 runtime 全面重命名为 control
- `481ce6f` 收口运行态动态文件并补齐 V2 数据库存储
- `6b2ea46` 统一运行态 SQLite 共享服务
- `3fe5a0e` 收口实例级控制与规则源运行路径

它带来的收益非常直接：状态真源清晰、控制面 API 稳定、健康检查和问题定位有统一入口，后续新增模块也不必再重复造一套状态持久化。

## 2. 数据面热路径优化：从“多做一点”变成“少做错事、少做重复事”

这一段最重要的不是“把所有路径都再并发一层”，而是把热点路径里无意义的重复工作和错误统计清掉。

在 `domain_mapper` / `query_context` 这条线上，优化重点是去重：快路径已经打过标记时跳过重复匹配，修复实例判断不严谨导致的重复执行，修复 fast mark 冲突并加固容量控制。代表提交是 `a9c62dd`、`be87ada`、`0762f2b`、`dc42e60`。这类优化的价值在于它发生在高频请求路径上，单次节省不大，但总体收益稳定且持续。

在 `aliapi` 这条线上，优化已经从“支持并发查询”演进成完整的查询治理：单上游快路径减少不必要分支和拷贝；多上游时做并发竞速，优先返回最早可用 `A/AAAA`；对连续失败、固定 SERVFAIL 热点、全上游超时引入状态化治理和上游熔断；query/error/winner/latency 变成可恢复的 runtime stats；最新还修正了 hedged query 场景下 `context.Canceled` 被误记为 upstream error 的统计口径。代表提交是 `4f5f630`、`77fb9d2`、`3b84a10`、`897ee29`、`3d8d9f2`、`a9e2084`。

在 `forward` 这条线上，插件从“依次转发”演进成了“带健康评分的竞速器”。`plugin/executable/forward/exchange.go` 体现了这套模型：先按 EWMA 延迟、inflight、failure 计算健康分，再按并发数裁剪 upstream，后续用 `hedgedQueryDelay` 分批发射 worker，避免一次性打满上游，再用 `responsePicker` 区分有效答复、NXDOMAIN、其他响应和纯错误，并将 winner 记入审计和 runtime stats。这套设计的价值不只是“快”，更在于它让竞速行为更可控、更可解释。

`sequence` 的优化偏向热路径收口：quick setup 更清晰，chain 执行更紧凑，并补齐 benchmark 作为证据。代表提交主要是 `4f5f630` 和 `77fb9d2`。

这一整条主线的共同点可以概括为：**不靠盲目并发提速，而是通过去重、快路径、延迟发射、winner 选择和正确统计，减少无意义工作和错误判断。**

## 3. 缓存系统优化：从内存缓存升级为可恢复缓存

缓存系统的变化并不是“加了一个 WAL 文件”这么简单，而是完成了从“内存态 + dump”到“snapshot + WAL + stats API + UI + 巡检脚本”的升级。核心机制在 `plugin/executable/cache/persistence.go`：snapshot 作为稳定基线，WAL 记录增量写入，启动时先加载 snapshot 再 replay WAL，周期 checkpoint 后清空 WAL，并通过结构化 stats API 暴露状态，再配套 `scripts/check_cache_stats.py` 做发布后巡检。

这套设计把 cache 从“性能插件”推进成了“有恢复能力的状态组件”。它后续又延伸到了 FakeIP 缓存语义重构、缓存策略拆分、stale / lazy update 路径观测、WAL 恢复边界加固，以及移除手工 GC 干预、回归 Go runtime 管理。代表提交包括 `9d8d078`、`941d257`、`24fa646`、`791c22f`、`e0a65b9`、`aa43fb2`。

为什么这算深度优化？因为它同时解决了缓存系统最麻烦的 3 个问题：崩溃后的恢复、线上状态的可信观测、缓存策略和真实业务语义的一致性。只做命中率优化解决不了这 3 件事。

## 4. requery / domain pool / memory pool：从离线动作变成后台工作流

这一段把“分流记忆刷新”和“重建任务”从人工动作改造成了可恢复、可持续运行的后台系统。覆盖内容包括：requery 执行流拆分、run / checkpoint / history / state 持久化、自动恢复和自动续跑、domain pool 存储与评分、memory pool 即时热规则发布，以及 hybrid 模式、dirty / cooldown / stale 机制。

`plugin/executable/requery/requery_recovery.go` 已经具备明显的状态机特征：`FullRebuildTask` 带 task id、stage、progress、primary/secondary 队列；中间状态可持久化；启动时能识别未完成任务；可按配置延时自动恢复。与此同时，`plugin/executable/domain_memory_pool/hot_publish.go` 体现了热规则闭环：add / replace 分离、debounce、pending queue、dispatch fail 记录、live 指标回写。换句话说，热规则发布不再是“每来一条马上推一次”，而是具备节流、聚合、失败追踪和 replace 回退能力。

代表提交包括 `1f58e04`、`30d2803`、`b725c10`、`2d015a6`、`8e0d669`、`120f781`、`e781dae`、`8ac30cd`、`f8b72ac`。这条线真正解决的问题是：重建任务不再怕中断，刷新策略从全量思维变成热点优先 + 巡检兜底，热规则发布不再容易抖动，状态、统计、控制面和 UI 被真正联通。

## 5. 规则源与 config v2：从历史兼容堆叠变成明确的编译模型

这条线把配置系统从“include + 旧键 + 历史约定”重构为明确的 V2 编译模型。`internal/configv2/compiler.go` 明确拒绝 `legacy`、`include`、`plugins`、`runtime` 这些旧键，再统一编译出 listeners、upstream groups、policies 和 control plugins。这说明 V2 不是旧配置的包裹层，而是新编译模型。

与此同时，`coremain/rule_source_service.go` 把规则源统一成了 service：load active config、写 custom config、refresh one / refresh all、reload 生效、记录 status。配合 `config/custom_config/*.yaml`，规则源、自定义配置、运行时控制开始有明确边界。代表提交包括 `cc09154`、`23b5f3e`、`7e22533`、`ac90032`、`1082324`、`33ab5ab`、`a6b16ff`、`bf4b1be`、`cfe0d4f`、`9e00ef6`。

为什么这算深度优化？因为它改变的是“系统如何被描述”以及“配置如何被编译成执行链路”。这是配置平面的抽象升级，而不是字段级改动。

## 6. 审计与可观测性：从“有日志”升级为“有结构、有搜索、有实时聚合”

审计系统在这段区间里被重构成了一个真正可用的观测子系统：SQLite 审计存储、collector + realtime store 分层、搜索 API、overview API、前端 v3，并与 query path 上的 upstream/cache/domain set 等信息打通。

`coremain/audit_collector.go` 展示了新的收集模型：每条请求在 query path 上构造结构化 `AuditLog`，一份写入 realtime store，一份进入异步 writer queue，队列满时标记 degraded，而不是静默假成功。这一点很关键，因为它符合 Debug-First：高压下暴露真实系统状态，而不是伪装成一切正常。

后续这条线又叠加了 SQLite 读写拆分、搜索 SQL、审计设置管理、保留策略维护、UI v3 与文档收口。代表提交包括 `1817acf`、`965b4f1`、`df23cf1`、`6ce93f0`、`f043594`、`698be0c`、`71bb6d0`、`66ecc1d`。结果是：审计不再是事后翻文本日志，而是可结构化查询、可做概览趋势、可显式暴露丢日志和降级状态的控制面能力。

## 7. API、控制面 UI 和运维：把系统变成真正可操作的产品

接口与 UI 的主线很清楚：逐步把旧插件级接口收口到核心 API，统一 JSON 返回结构，让前端改吃稳定 API，再引入 OpenAPI / Swagger，并同步重构审计页、cache/shunt 面板和系统控制页。代表提交包括 `45df5a5`、`407f933`、`05bdf81`、`013b2b9`、`53ab249`、`30c4cf9`、`2bb35be`、`5ae3bba`、`fc179c2`、`ca4cd89`。它的价值在于：控制面开始摆脱内部实现细节，形成真正稳定的外部契约。

运维与稳定性这条隐线同样重要。代表动作包括：`c52c3ed` 修复 ARM 上未对齐原子访问，`437f67e` 修复 DDNS 热加载与 stale cache 行为，`8096552` 修复 override 并发保存并加固前端防重入，`f9fbaf6` 重构重启与更新链路，`2830fd6` 增加 health 自检命令与接口，`98fe033` 为关键管理动作增加 SQLite 事件审计，`e0dfddb` 新增 DNS 压测工具链。最近的 `a9e2084` 也属于这一类：它修复了 hedged query 场景下 `context.Canceled` 被误记为 upstream error 的统计口径问题，避免慢上游被系统性误判成高错误率。这类修复不是让面板更好看，而是让控制面更接近真实系统行为。

## 为什么我把这段区间定义为“深度优化”

我认为这段区间最有价值的，不在某一个 benchmark 数字，而在 3 件更难的事情。

第一，把隐式状态变成显式状态。没有显式状态，系统只能靠日志猜；有了显式状态，才能做恢复、审计、巡检、健康检查、UI 和稳定 API。

第二，把局部快变成全链路稳。单点提速很容易，但如果没有熔断、失败抑制、WAL、checkpoint、自动恢复、健康检查，那只是快而脆。这段优化明显是在追求“快且稳”，而不是“快但脆”。

第三，把实现细节从外部契约中剥离。通过 config v2、custom_config、control.db、统一 API、OpenAPI、外置 UI，系统开始具备真正的演进空间。以后要继续扩展，不必再一边背旧包袱、一边硬叠补丁。

## 最终结论

如果要给 `02aa791..HEAD` 这一段起一个准确名字，我认为最贴切的不是“性能优化期”，而是：

**V2 架构落地期 + 数据面治理期 + 控制面成型期**

因为这 179 个提交真正完成的是：

- 边界重画
- 真源重定
- 状态收口
- 故障治理
- 观测闭环
- 运维落地

这就是为什么我会把这段区间定义为“深度优化”，而不是“多做了很多功能”。
