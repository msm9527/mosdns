# DNS 失败治理方案

## 文档目标

这份文档定义当前 mosdns 在生产运行中面对 `SERVFAIL`、`NXDOMAIN`、超时和热点异常域名时的治理方案。

目标不是“把所有失败都消掉”，而是做到：

- 失败分类清晰
- 负结果处理可控
- 热点异常不放大
- 上游质量问题可观测
- 缓存收益和失败复用可区分
- 对真实用户请求的影响最小

## 当前现状

结合最近压测与链路复盘，当前系统已经具备：

- 正常的正向缓存能力
- 分流记忆与刷新链路
- 压测报告中的冷启动与缓存阶段拆分

但仍然存在一类典型问题：

- 少量热点域名在重复访问阶段持续出现 `SERVFAIL`
- 一批域名本身就是 `NXDOMAIN` / `nov4`
- 若不拆分统计，很容易把“负结果复用”误看成“缓存退化”

最近一轮压测报告：

- [stress_report_20260306_202656.json](./../reports/stress_report_20260306_202656.json)
- [stress_failures_20260306_202656.ndjson](./../reports/stress_failures_20260306_202656.ndjson)

关键结论：

- `udp-cold`
  - `positive_rate = 95.7847%`
  - `timeout_rate = 3.8577%`
- `udp-cache-hit`
  - `positive_rate = 95.6688%`
  - `timeout_rate = 0.6907%`
  - `servfail_rate = 2.7575%`

这说明：

- 缓存显著降低了超时
- 但缓存阶段混入了较多负结果，尤其是 `SERVFAIL`

## 专业 DNS 服务应有的失败分类

专业 DNS 服务不应该把所有失败都混成一类。

建议至少分成四类：

### 1. 正常负结果

典型：

- `NXDOMAIN`
- `NODATA`
- `AAAA` 无记录
- A 记录路径上的 `nov4`

特征：

- 多个健康上游结果一致
- 重复查询结果稳定
- 不属于故障

治理原则：

- 允许缓存
- 单独统计
- 不计入“服务异常”

### 2. 可接受的权威异常

典型：

- 权威链路返回稳定 `SERVFAIL`
- 域名配置本身错误
- 上游和公共 DoH 都返回 `SERVFAIL`

特征：

- 跨上游一致失败
- 不是某条分流链路单点问题

治理原则：

- 允许短期失败缓存
- 不做路径切换重试风暴
- 记录为“外部失败”

### 3. 上游不健康或链路异常

典型：

- 某个递归上游超时
- 某个上游慢 `SERVFAIL`
- 同域名在不同上游结果明显不一致

特征：

- 与上游集合相关
- 切换上游后可明显改善

治理原则：

- 上游熔断
- 上游摘除
- 更短上游超时
- 并发抢答后快速收敛

### 4. 分流或策略错误

典型：

- 域名走错链路后失败
- 国内上游失败但国外公共解析正常
- fakeip/realip 策略错误导致负结果被放大

特征：

- 在另一条明确链路上能稳定得到正确结果
- 当前链路结果与策略目标不一致

治理原则：

- 修分流规则
- 修旁路刷新验证
- 修分流记忆晋升条件

## 当前 mosdns 的问题归类

基于已经完成的复盘，当前问题大致分两类：

### A. 正常负结果组

例如：

- `123juzi.net`
- `126.link`
- `315che.com`
- `3234.com`
- `4399api.net`
- `ap-southeast-2.myhuaweicloud.com`
- `ap-southeast-3.myhuaweicloud.com`
- `apaas-zone-test.com`

特征：

- 国内链路返回 `NXDOMAIN`
- 公共 DoH 也返回 `NXDOMAIN`
- 一部分已经进入 `nov4list`

结论：

- 不是误分流
- 允许作为负结果缓存
- 不应计入“缓存异常”

### B. 全局 `SERVFAIL` 组

例如：

- `114menhu.com`
- `bihu.com`
- `alimei.com`
- `coindog.com`
- `bcyimg.com`

特征：

- 国内链路 `SERVFAIL`
- 公共 DoH 也返回 `SERVFAIL`
- 不属于单纯的国内上游误判

结论：

- 更像权威侧或外部解析链路异常
- 不是当前分流规则的主问题
- 应进入失败治理，而不是简单改路由

## 专业 DNS 服务建议的治理层

建议把失败治理拆成五层。

## 第一层：结果分类缓存

缓存不能只区分“成功”和“失败”。

建议最少按下面四类处理：

- `positive`
- `nxdomain`
- `servfail`
- `timeout/transport_error`

推荐策略：

- `positive`
  - 按正常 TTL 或 lazy cache 策略
- `nxdomain`
  - 允许负缓存
  - TTL 建议 `60s ~ 300s`
- `servfail`
  - 不建议长期缓存
  - 只做短期失败缓存
  - TTL 建议 `15s ~ 60s`
- `timeout/transport_error`
  - 默认不入长期缓存
  - 只允许极短抑制窗口
  - 建议 `5s ~ 15s`

目标：

- 防止热点异常域名重复打爆上游
- 同时避免把瞬时故障长期固化

## 第二层：失败抑制与重试去抖

对热点域名，最怕的不是一次失败，而是：

- 同一个域名在高并发下同时失败
- 每个请求都去重新打上游

建议：

- 同域名失败请求合并
- 同域名在短窗口内只允许一个“主动刷新”
- 其余请求直接复用最近失败结果或等待共享结果

推荐参数：

- `SERVFAIL` 共享窗口：`15s ~ 30s`
- 超时共享窗口：`5s ~ 10s`
- 单域名并发刷新数：`1`

## 第三层：上游健康治理

专业 DNS 服务必须把“坏上游”和“坏域名”分开。

建议新增上游健康评分：

- 最近超时次数
- 最近慢响应次数
- 最近 `SERVFAIL` 占比
- 熔断窗口

推荐策略：

- 连续超时达到阈值后临时摘除
- 慢 `SERVFAIL` 达到阈值后降低优先级
- 周期性探活恢复

推荐默认值：

- 上游超时阈值：`3`

## 当前实现状态

当前版本已经落地以下最小可用治理能力：

- `cache` 插件支持独立配置 `nxdomain_ttl` 与 `servfail_ttl`
- `aliapi` 支持 `failure_suppress_ttl`、`upstream_failure_threshold`、`upstream_circuit_break_seconds`
- `aliapi` 支持 `persistent_servfail_threshold` 与 `persistent_servfail_ttl`，用于处理 `114menhu.com` 这类跨窗口重复 `SERVFAIL` 热点
- 当并发上游全部以 transport error / timeout 失败时，会返回短期 `SERVFAIL`，避免热点失败域名持续穿透到上游
- 上游连续 transport error 达到阈值后会短期熔断，窗口结束后自动恢复探测

当前仍未落地的后续项：

- 更细的上游健康评分与慢失败降权
- 将 `SERVFAIL` / `NXDOMAIN` / 正向命中拆到运行时 `/stats`
- 基于旁路验证的误分流纠偏自动化
- 热点失败域名 TopN 与长抑制窗口的运行态暴露
- 熔断时间：`60s`
- 慢响应阈值：`>800ms`
- 恢复探测间隔：`30s`

## 第四层：失败结果统计与报表分离

生产 DNS 的报表不能只看 `response_rate`。

建议固定输出：

- `positive_rate`
- `nxdomain_rate`
- `servfail_rate`
- `timeout_rate`
- `transport_error_rate`

并单独给出：

- `cache_effect`
- `negative_reuse_rate`
- `servfail_hotspot_topn`

这样可以区分：

- 缓存收益差
- 负结果多
- 上游异常多

## 第五层：策略纠偏入口

对于“当前链路失败、但另一条链路成功”的域名，必须有专门纠偏入口。

建议：

- 将这类域名单独归类为 `route_suspect`
- 不直接推进 `realiplist/fakeiplist`
- 先走旁路验证
- 连续多次验证后再决定是否改分流

这一层服务的是“误分流治理”，不是“全局失败治理”。

## 当前 mosdns 的推荐落地顺序

基于当前现状，建议按这个顺序做。

### Phase 1：立即可做

目标：先把热点失败域名对服务的冲击降下来。

建议：

- 为 `SERVFAIL` 增加短期失败缓存
- 为超时增加更短的失败抑制窗口
- 压测报告继续保留当前 `breakdown + cache_effect`
- 在 UI 或日志里增加 `SERVFAIL hotspot` 统计

推荐参数：

- `SERVFAIL` 失败缓存：`30s`
- 超时抑制：`10s`
- 单域名失败并发刷新：`1`

### Phase 2：中期应做

目标：把上游问题和域名问题拆开治理。

建议：

- 给 `aliapi` 或上游调度层增加健康评分和熔断
- 记录每个上游的：
  - timeout
  - slow response
  - servfail ratio
- 对国内上游做自动降权

### Phase 3：长期应做

目标：把失败治理纳入分流决策。

建议：

- 新增 `route_suspect` 状态
- 旁路验证“当前链路失败、替代链路成功”的域名
- 只对真正存在稳定收益的域名改分流

## 当前不建议做的事

下面这些做法看起来能让报表更好看，但不专业：

- 因为 `SERVFAIL` 多，就直接把整批 CN 域名切到国外链路
- 因为负结果多，就禁止缓存所有负结果
- 为了压低失败率，把 `NXDOMAIN` 和 `SERVFAIL` 从统计里直接去掉
- 对瞬时超时做长 TTL 缓存

这些都会带来更大的副作用。

## 推荐默认值

如果要给出一版生产级默认建议，推荐如下：

```yaml
failure_policy:
  nxdomain_cache_ttl: 120s
  servfail_cache_ttl: 30s
  timeout_suppress_ttl: 10s
  per_domain_refresh_concurrency: 1

upstream_health:
  timeout_threshold: 3
  circuit_break_duration: 60s
  slow_response_ms: 800
  recovery_probe_interval: 30s

reporting:
  expose_positive_rate: true
  expose_nxdomain_rate: true
  expose_servfail_rate: true
  expose_timeout_rate: true
  expose_cache_effect: true
  expose_servfail_hotspot_topn: 20
```

## 最终结论

当前 mosdns 不是“缓存坏了”，也不是“分流缓存学错了”。

当前真正缺的是一层更专业的失败治理：

- 对负结果分级处理
- 对 `SERVFAIL` 短期抑制
- 对坏上游熔断
- 对误分流单独验证
- 对统计口径拆分

这才是一个专业 DNS 服务应有的方案。
