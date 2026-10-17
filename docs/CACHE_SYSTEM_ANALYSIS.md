# 当前缓存系统设计与 App Store 异常规避说明

## 1. 文档目的

这份文档分成两部分：

1. 只描述当前仓库里的缓存系统设计。
2. 单独说明这套设计如何规避 `Apple App Store 长时间运行后出现网络异常` 这一类问题。

本文讨论的缓存包括：

- `type: cache` 的 DNS 响应缓存。
- `udp_server` 内部的 UDP 快路径缓存。

---

## Part I. 当前缓存系统设计

## 2. 总体结构

当前仓库里实际有两套缓存：

### 2.1 响应缓存

对应实现：

- [plugin/executable/cache/cache.go](../plugin/executable/cache/cache.go)

这是主缓存，也是当前系统里最核心的一套缓存。它负责：

- 缓存完整 DNS 响应。
- 支持 stale/lazy refresh。
- 支持 snapshot + WAL 持久化。
- 支持运行态观测、按域名清理和全量清空。

### 2.2 UDP 快路径缓存

对应实现：

- [plugin/server/udp_server/udp_server.go](../plugin/server/udp_server/udp_server.go)

这是一套独立缓存，只服务于 UDP fast path。它的目标是：

- 缩短热点查询的极短路径延迟。
- 为旁路 stale refresh 提供配置决定的热点保留窗口。

它不是响应缓存的 L1，也不是同一个数据结构。

## 3. 配置真源与装配入口

缓存策略的系统级真源是：

- [config/sub_config/cache_policies.yaml](../config/sub_config/cache_policies.yaml)

启动时由：

- [coremain/sub_config_cache_policies.go](../coremain/sub_config_cache_policies.go)
- [coremain/mosdns.go](../coremain/mosdns.go)

读取、合并默认值，再把策略注入到具体插件。

当前支持两类策略：

1. `response`
   - 作用于 `type: cache`
2. `udp_fast_path`
   - 作用于 `udp_server` 的快路径缓存

## 4. 响应缓存设计

## 4.1 逻辑分层

当前响应缓存不是单层结构，而是由下面几层能力组成：

1. `L1` 热缓存。
2. `L2` 主缓存。
3. lazy stale / lazy refresh。
4. snapshot + WAL 持久化。
5. runtime API 与统计。

## 4.2 Key 组成

响应缓存 key 不是简单的 `qname + qtype`，而是由以下字段拼成：

- `AD/CD/DO` 标志位
- `QTYPE`
- `QNAME`
- 可选的 `ECS`

影响：

- 开启 `enable_ecs` 的实例会把 ECS 纳入 key。
- 不同 `DO` 位的请求不会共享缓存。
- 当前实现没有把 `QCLASS` 单独放进 key。

这些持久化 key 用于递归 IN 查询。其他 DNS class 和非递归请求直接交给后续解析链，不复用或覆盖普通请求的缓存。

客户端 DO 标志在规范化 OPT 时保留到上游；key 同时考虑原始客户端和当前上游查询的 DO。恢复旧 dump、WAL 或导入缓存时，响应必须对应递归 IN 查询，避免旧版遗漏 class/RD 的 key 带回不兼容结果。这不代表本机执行 DNSSEC 签名验证。

## 4.3 缓存对象

L2 里实际保存的是 `item`：

- `resp`
  - 打包后的 `dns.Msg` 二进制
- `storedUnixNano`
  - 写入时间
- `expireUnixNano`
  - DNS 响应自身的真实过期时间
- `domainSet`
  - 路由标签、依赖标签和 revision 签名元数据

需要区分两个时间：

1. `expireUnixNano`
   - 表示真实 DNS TTL 何时过期。
2. backend 的 `cacheExpirationTime`
   - 表示这条缓存还能在系统里继续保留多久。
   - 开启 `lazy_cache_ttl` 后，它通常会比真实 TTL 更长。
3. lazy stale 的返回窗口
   - 由 `lazy_stale_ttl` 控制。
   - 它只决定过期后还能直接返回旧答案多久，不决定缓存条目在 backend 中保留多久。

## 4.4 L1 热缓存

L1 是热点层，特点如下：

- 固定 `256` 个 shard。
- shard 内采用 `CLOCK` 风格环形淘汰。
- 保存的是对 L2 `item` 的共享引用，不复制响应对象。
- L2 命中且未过期时，会晋升到 L1。

L1 主要解决的是：

- 热点查询的锁竞争。
- 高频 key 的重复 L2 查找成本。

## 4.5 L2 主缓存

L2 基于：

- [pkg/cache/cache.go](../pkg/cache/cache.go)

其底层并发存储来自：

- [pkg/concurrent_map/map.go](../pkg/concurrent_map/map.go)

这里有一个重要实现事实：

> 当前 L2 不是严格 LRU。

虽然配置注释里写了“按 LRU 淘汰”，但真实实现是：

- `pkg/cache.Cache` 负责 TTL 包装。
- 容量限制来自 `concurrent_map.NewMapCache(size)`。
- shard 超限时，会在 shard map 上删掉遍历到的旧键，不维护严格访问顺序。

更准确的理解应当是：

- `L1`：CLOCK 热点缓存。
- `L2`：分片并发 map + TTL + 非严格 LRU 淘汰。

## 4.6 读取路径

响应缓存执行链路可概括为：

1. 构造缓存 key。
2. 先查 `L1`。
3. `L1` 未命中或失效时再查 `L2`。
4. 如果命中 fresh 结果，直接返回。
5. 如果命中过期结果：
   - 普通域名可直接返回 stale，并后台刷新。
   - 特殊域名可以走“先刷新，不直接返 stale”的路径。
6. miss 时继续执行后续 sequence。
7. 下游执行返回响应后，按策略写回 `L2`，并同步更新 `L1`。

## 4.7 写入路径

命中 miss 后，响应会根据 `rcode` 和 TTL 进入缓存：

### 成功响应

- `msgTtl = 最小 RR TTL`
- 若设置了 `lazy_cache_ttl`
  - `cacheTtl = lazy_cache_ttl`
- 否则
  - `cacheTtl = msgTtl`

### 空答案成功响应

- 真实 `msgTtl` 最多裁剪到 `300s`；存在 SOA 时还受 `min(SOA TTL, SOA MINIMUM)` 约束。
- `cacheTtl = msgTtl`，不应用 `lazy_cache_ttl`、`domain_set_ttl` 或客户端最小 TTL。
- CNAME 链末尾携带 SOA、且没有请求类型记录的 NODATA 采用相同规则。
- 过期后不返回 stale 负响应。

### `NXDOMAIN`

- 使用 `nxdomain_ttl`，并以 SOA TTL 和 MINIMUM 的较小值为上限。
- 不应用长保留、域名集 TTL 覆盖或 stale 返回。

### `SERVFAIL`

- 使用 `servfail_ttl`

### 最小下限

没有 SOA 约束且算出的 TTL 小于等于零时，使用 `5s` 短时保护。SOA TTL 或 MINIMUM 为零的负响应不缓存。长保留、`domain_set_ttl` 和客户端 TTL 下限只适用于正响应。

### 携带 RRSIG 或上游 AD 的响应

- 不抬高上游 TTL，以最小 RR TTL、RRSIG Original TTL 和签名剩余有效期的最小值限制缓存寿命及缓存副本的 TTL。
- 不应用客户端 TTL 下限、长缓存覆盖或 stale 返回；签名尚未生效或已经过期时不写入缓存。
- 只有 AD、没有 RRSIG 的响应同样遵守原始 RR TTL，避免普通 TTL 治理延长上游认证结果的有效期。
- 这些限制用于避免缓存延长签名数据的寿命，不代替 DNSSEC 签名验证。
- 所有 stale 响应清除 AD；即使普通正响应仍在配置的保留窗口内，原始 TTL 到期后也不再保留 AD。

## 4.8 lazy stale 与 lazy refresh

当前实现里的 lazy stale 有三个层面：

1. 后台保留窗口
   - 由 `lazy_cache_ttl` 决定。
2. 过期后允许直接返回旧答案的窗口
   - 由 `lazy_stale_ttl` 决定。
3. 返回给客户端的 stale TTL
   - 上限为 `5s`，更低的 `client_ttl_max` 仍会生效。
   - 不应用 fresh 响应的客户端最小 TTL；消息和预打包 wire 路径保持一致。

也就是说：

- `lazy_cache_ttl` 决定记录还能在缓存里保留多久。
- `lazy_stale_ttl` 决定真实 TTL 过期后还能返回旧记录多久。
- 客户端看到的 TTL 很短，目的是促使后续尽快重新请求。

当普通域名首次命中过期缓存时，当前行为通常是：

1. 先返回 stale。
2. 启动后台刷新。
3. 后续短时间内的再次请求，有机会拿到 fresh 结果。

此外还有 prefetch：

- 对接近过期的热点项，缓存会提前触发后台刷新。

### 4.8.1 多级缓存中的 stale 传播保护

主解析链路可能出现 `main_cache -> branch_cache -> upstream` 这种多级缓存结构。

这里有一个额外保护：

- 如果下层缓存返回的是 lazy stale 响应，上层缓存只把它透传给当前请求。
- 上层缓存不会把这个 stale 响应重新写成自己的新缓存项。

这个约束的目的不是降低缓存命中，而是避免下面这种放大：

1. 下层缓存里有一个已经过期但仍在 stale 窗口内的 CDN IP。
2. 上层缓存刚好 miss。
3. 如果上层把下层 stale 当成新结果保存，就会人为延长旧 IP 的可见时间。

当前实现通过 query context 标记 stale 来源，在 `saveRespToCache` 前拦截这类写入。

标记也会从 `EntryHandler` 传递到 UDP 快路径：解析后的消息与 `ResponsePayload.Stale` 都能保留 stale 来源。UDP 请求返回和 `refreshExpiredCache` 后台刷新均只透传 shared stale，不把它重新存成 fast fresh 记录。这样可避免共享缓存的旧 IP 被重新赋予 `7200s` 或域名集覆盖的内部寿命。

## 4.9 路由签名与依赖失效

当前缓存不仅记录响应，还记录了和路由相关的元数据：

- 当前 `domainSet`
- cache dependency set
- 依赖对象的 revision 签名

对应实现：

- [plugin/executable/cache/route_signature.go](../plugin/executable/cache/route_signature.go)

作用是：

- 如果域名当前命中的路由标签变了，旧缓存要绕过。
- 如果依赖的规则/列表 revision 变了，旧缓存也要绕过。

这套机制解决的是：

- “缓存内容本身没过期，但它现在已经不该走原来的分流路径” 这类问题。

## 4.10 域名标签对象池

`domainSet` 字符串会进入 intern pool：

- [plugin/executable/cache/domain_set_pool.go](../plugin/executable/cache/domain_set_pool.go)

目的：

- 降低重复标签字符串的内存占用。

在删除、flush 和淘汰时会释放引用。

## 4.11 持久化设计

对应实现：

- [plugin/executable/cache/persistence.go](../plugin/executable/cache/persistence.go)

当前持久化由两部分组成：

### snapshot

特点：

- gzip
- protobuf block
- 原子写入 + rename

内容包括：

- key
- `cacheExpirationTime`
- `msgExpirationTime`
- `storedTime`
- `resp`
- `domainSet`

### WAL

当前 WAL 记录

- `store`
- `delete`
- `flush`

持久化边界

- 新写入/覆盖会追加 WAL。
- 域名批量失效先追加删除记录并执行 Flush 与 Sync，再删除内存和 L1 条目。批次不会立即重写全量快照。
- 清空先同步 WAL 的 flush 记录，再清空内存，并保留显式快照保存。
- checkpoint 在变更锁内同步旧 WAL，采集不可变快照元数据，并记录完整 WAL 边界。gzip 与快照落盘在锁外执行，期间的写入、删除、清空和 dump 导入继续追加旧 WAL。快照同步并原子替换后，再在变更锁内同步旧 WAL，将采集边界之后的完整增量复制到带 header 的临时 WAL，同步并原子替换 WAL，最后同步父目录。
- 若进程在快照替换和 WAL 替换之间退出，启动会用快照记录的 UUID 与逻辑 cut 跳过已覆盖的旧 WAL 前缀，只重放增量。已完成的删除和清空不会复活，也不会因重复插入触发错误容量淘汰。新代码兼容 v1 WAL，并在开始服务前将其原样迁移为 v2；降级必须恢复成对备份，具体合同见 [Cache WAL 灰度发布与回滚手册](./CACHE_WAL_ROLLOUT.md)。

### 恢复顺序

重启恢复时：

1. 先读 snapshot。
2. 再回放 WAL。

导入 dump 和恢复 snapshot/WAL 时重新检查负响应及携带 RRSIG 或 AD 的响应。旧版本记录的较长过期时间会按原始存储时间及当前 TTL 上限收紧；已过期、零 SOA 寿命和截断记录直接丢弃，避免升级后复活旧错误结果。

WAL 恢复同时重建二进制响应的 TTL 偏移索引，保证 UDP 缓存命中后仍按实际存储时间递减 TTL。

恢复或 v1 WAL 迁移失败时，已部分载入的旧条目会被清空，原持久化文件保持隔离，错误保留在运行态统计和日志中。后续上游返回的新响应仍正常进入内存缓存；内存 purge/flush 继续生效，显式保存和导入保持阻断，直到修复存储并重启实例。这样持久化故障不会使共享缓存永久变成每次冷查。

### dump 触发特点

周期 dump 不是每次到点都写，而是：

- 到了 `dump_interval`
- 且 `updatedKey >= 1024`
- 才真正触发 dump

这是一种减少频繁落盘的策略。

批量失效会累计变更计数，等待满足条件的周期 checkpoint，或由显式保存和退出时的保存合并执行。WAL 删除记录即时同步，普通写入按 `wal_sync_interval` 合并同步，默认间隔为 60 秒，正常关闭仍同步尾部。

## 4.12 运行态 API 与观测

对应实现：

- [coremain/api_cache.go](../coremain/api_cache.go)
- [plugin/executable/cache/stats.go](../plugin/executable/cache/stats.go)

当前主要接口有：

- `GET /api/v1/cache/{tag}/stats`
- `GET /api/v1/cache/{tag}/entries`
- `POST /api/v1/cache/{tag}/save`
- `POST /api/v1/cache/{tag}/flush`
- `POST /api/v1/cache/{tag}/purge_domain`
- `POST /api/v1/cache/purge_domains`
- `POST /api/v1/cache/flush_all`

最关键的观测字段包括：

- `backend_size`
- `l1_size`
- `updated_keys`
- `hit_total`
- `l1_hit_total`
- `l2_hit_total`
- `lazy_hit_total`
- `lazy_update_total`
- `lazy_update_dropped_total`
- `last_dump`
- `last_load`
- `last_wal_replay`

## 4.13 运行态失效联动

`requery` 发布结果后，会对 runtime cache 做联动失效：

- 域名数量较少时，按域名精确 purge
- 域名数量较多时，直接 fallback 到全量 flush

对应实现：

- [plugin/executable/requery/cache_invalidation.go](../plugin/executable/requery/cache_invalidation.go)

这说明当前系统已经具备：

- “按域名清理缓存”
- “发布后自动收敛旧缓存”

而不只是依赖手工重启。

## 5. UDP 快路径缓存设计

UDP 快路径缓存与 `type: cache` 完全独立。

快路径的响应 key 不包含 DNSSEC、ECS、Cookie 等请求参数，因此只有普通递归 IN 查询和无选项、无标志的 EDNS 查询可以使用它。携带 AD/CD/DO、EDNS 选项或非零 EDNS 版本的查询走完整解析链；这些查询的响应也不写入普通快缓存。空 EDNS 查询仍使用快路径，响应长度遵守客户端声明的 UDP 大小，无 EDNS 时上限为 512 字节；较大响应交由完整链处理截断与 TCP 重试。

普通查询与空 EDNS 查询共享快缓存时，根据当前请求增删尾部空 OPT。包含其他 additional 记录或复杂 OPT 的响应不进入快缓存；常见命中不需要额外解析或分配。

后台刷新拿到新鲜的 AD、RRSIG、NXDOMAIN 或 NODATA 响应时，即使该结果不能进入快缓存，也会撤销本次被刷新的旧响应。失败、截断或来自下层 stale 缓存的结果不会触发这项撤销；并发产生的新缓存项不受旧刷新任务影响。

### 5.1 缓存对象

快路径缓存保存的是：

- 预裁剪 TTL 后的二进制响应包

### 5.2 缓存范围

当前只缓存具有答案的正 `NOERROR` 响应。

不会缓存：

- `NXDOMAIN`、空 `NOERROR`，以及 CNAME + SOA 的 NODATA
- `SERVFAIL`、截断响应和非法响应
- 携带 RRSIG 或上游 AD 的响应，由共享响应缓存管理其认证寿命

负响应由共享响应缓存按短 TTL 处理，避免 UDP 独立的内部 TTL 和 stale 窗口延长故障。

UDP stale 响应会清除 AD；普通正响应的原始 TTL 到期后同样清除 AD，避免热点保留策略延长认证标记。

fast 自身命中过期项时，直接回复和 `CopyResponse` 都将非 OPT 记录 TTL 限制到不超过 `5s`，并保留原本小于 `5s` 的 TTL。处理只修改发出的副本，OPT 的扩展状态与标志位、保存的 fresh wire 均不改变。

后台刷新按每个缓存项最近一次刷新预约时间计算 `stale_retry_seconds`，使用原子 deadline 保证同一重试窗口只预约一次。重试时钟不从条目过期时间计算，避免旧条目在高并发下为每个请求重复创建刷新任务。失败后仍可在下一窗口重试；成功替换的新条目具有独立状态。域名集 stale 窗口只在过期路径查询，普通 fresh 命中不重复查找该覆盖策略。

从 UDP 快路径转交完整 handler 后，`CopyResponse` 会再次校验当前条目的最大 stale 窗口及域名集覆盖，防止调度延迟或缓存代际替换后继续返回超出期限的旧地址。

### 5.3 过期语义

规则匹配与响应缓存分开处理：每个编译规则快照保留最多 8192 个域名的匹配证据（含未命中），避免热查询重复扫描正则。动态 provider 仍在每次查询中校验；热规则发布直接影响下一次查找；重建规则会替换快照及其缓存。该缓存不保存最终的动态分流决定。

节点探测链通过 `qname` 引用的生成域名集同样在查询时校验记忆池状态。过期、dirty 或未确认的 noV4/noV6 规则不能继续阻断解析；分类刷新可绕过负规则，后台查询不增加用户活跃计数。域名集内独立配置的静态规则仍按用户配置匹配。

默认策略面向热点查询：

- `internal_ttl = 7200s`
- `stale_retry_seconds = 30s`
- `stale_max_seconds = 7200s`
- `ttl_min = 120`
- `ttl_max = 900`
- 高变化域名使用独立的 12 小时热点保留和 stale 窗口。以上均只适用于正响应，部署时以 `cache_policies.yaml` 为准。

因此它更像是：

- 服务端短期热点缓冲
- 有上限的 stale-refresh 辅助层

这里的 `internal_ttl` 是独立的 fast fresh 保留时间，并不等于上游 RR TTL。普通无 AD/RRSIG 的短 TTL 正响应仍可能在此窗口内被当作 fresh 使用；高变化域名的 12 小时覆盖也保留。stale 传播保护只阻止已被共享缓存标记为 stale 的结果重新入 fast，不改变这些 fresh 策略。短客户端 TTL 不能单独保证 CDN 地址及时更新。

## 6. 当前设计的几个关键特征

可以把当前缓存系统概括成下面几条：

1. 响应缓存是“分层 + stale + 持久化 + runtime control”的完整系统，不是普通 map cache。
2. 路由变化和依赖 revision 已经被纳入缓存失效条件，不只是 TTL 到期才失效。
3. 持久化默认开启后，缓存行为会跨重启延续。
4. UDP 快路径缓存是独立短缓存，不应和响应缓存混为一谈。
5. 当前 L2 的真实淘汰语义不是严格 LRU，这一点需要在设计说明里明确。

---

## Part II. 当前实现如何避免 App Store 长时间运行异常被缓存长期放大

## 7. 问题本质

`Apple App Store 长时间运行后网络异常` 这类问题可能受到以下缓存行为影响：

1. Apple / App Store / CDN 相关域名变化快。
2. 某些解析结果真实 TTL 很短。
3. 如果 DNS 层仍把过期结果继续提供给客户端，即使只给 `5s`，客户端第一次访问也可能命中 stale 地址。
4. 后台刷新回来后，后续再点刷新又恢复。

高变更域名与长 fresh 保留、stale-serve 策略可能发生冲突，但不能仅凭这些代码确认某次 Apple 故障的原因。还需核对真实 DNS 回答、上游链路、IPv6 连通性与实际连接结果。

## 8. 当前实现里已经存在的规避点

当前实现并不是通过“Apple 专项规则”来处理这类问题，而是通过几项通用缓存边界控制，避免 stale 结果或失败结果在缓存层被长时间放大。

## 8.1 stale 返回时间被硬限制为短窗口

当前实现即使命中过期缓存，也不会把 `lazy_cache_ttl` 或 `lazy_stale_ttl` 原样返回给客户端。

真实行为是：

1. `lazy_cache_ttl`
   - 决定成功响应还能在 backend 里保留多久。
2. `lazy_stale_ttl`
   - 决定真实 TTL 过期后还能直接返回旧结果多久。
3. stale 响应真正回给客户端的 TTL 不超过 `5s`，不再被 fresh 的 `120s` 最小 TTL 抬高。
4. shared stale 的来源跨 `EntryHandler` 保留；UDP 请求返回及后台 refresh 均拒绝把它重新存入 fast。

这一步的作用是：

- 避免客户端长期持有旧地址。
- 即便第一次请求撞上 stale，客户端也会很快再次发起请求。

对 App Store 这类问题来说，它起到的是“缩短坏结果暴露时间”的作用，而不是“完全不返回 stale”。

## 8.2 命中过期缓存时会立即触发后台刷新

当前实现不是简单地把 stale 回给客户端后就结束，而是会马上启动 lazy refresh：

1. 首次过期命中时，返回短 TTL stale。
2. 同时启动后台刷新。
3. 后续请求如果赶上刷新完成，就会拿到 fresh 结果。

相关保护还包括：

- lazy refresh 有并发去重，避免同一 key 被重复刷新。
- 后台刷新有超时控制，默认 `5s`。
- 刷新完成后，后续请求可以从共享缓存拿到 fresh 结果；刷新期间仍可能返回 stale。

对 App Store 这类问题来说，这一步把现象从：

- “长期持续错误”

压缩成更接近：

- “第一次可能异常，后续快速恢复”

## 8.3 prefetch 会在真正过期前提前刷新热点项

当前实现对接近过期的热点项会做 prefetch：

- 条目还没过期时，就提前触发后台刷新。

这一步的作用是：

- 降低客户端真正撞上过期项的概率。
- 对热点域名更有效，因为它们更容易在临近过期时再次被访问。

因此，虽然它不是 Apple 专项逻辑，但对 App Store 这类高频访问的热点域名，本身就是一种已有的缓解机制。

## 8.4 `SERVFAIL` 不会被长 lazy 窗口放大

这是当前实现里一个非常关键的保护点。

`SERVFAIL` 的缓存语义和成功响应不同：

- 它不会使用 `lazy_cache_ttl` 那套长保留窗口，也不会进入 `lazy_stale_ttl` stale 返回窗口。
- `msgTtl` 和 `cacheTtl` 都直接取 `servfail_ttl`。

这意味着：

- 上游临时失败不会像成功响应那样被保留数小时甚至更久。
- 临时故障更容易自行恢复，不会被长时间固化在成功缓存窗口里。

对 App Store 这类问题来说，这一步避免的是：

- Apple 某个上游或 CDN 节点短暂异常
- 然后失败结果被系统长时间放大复用

## 8.5 UDP 快路径不会长时间放大失败结果

`udp_server` 的快路径缓存本身也带有防放大边界：

- 仅缓存具有答案的正 `NOERROR` 响应
- 负响应、空响应和截断响应交给共享缓存处理
- 正响应的热点保留和 stale 参数由 `cache_policies.yaml` 控制，不能用于推断负响应寿命

这意味着：

- UDP fast path 拒绝负响应和 shared stale 入库；自身的正响应 fresh 保留仍按 `internal_ttl` 和域名集覆盖执行。
- 刷新失败时不会无限返回旧结果。

## 8.6 路由变化和依赖 revision 变化会绕过旧缓存

当前实现里，缓存命中不仅看 key，还会检查：

- 当前 `domainSet`
- 依赖规则的 revision 签名

只要发现：

- 路由标签变了
- 依赖的规则/列表版本变了

旧缓存就会被绕过。

这一步缓解的是另一类和 App Store 体验类似的问题：

- 规则明明已经变化
- 但客户端仍然在吃老路径下的缓存结果

## 8.7 当前实现支持运行态精确清理，不需要只能靠重启恢复

当前实现已经内建：

- 按域名 purge 响应缓存
- 按域名 purge UDP fast cache
- 全量 flush

以及 `requery` 发布后的 runtime cache invalidation。

这意味着：

- 运行中一旦确认是缓存问题，不一定非要重启进程。
- 系统本身已经提供了直接删除旧缓存的路径。

这虽然属于运维控制面能力，但它也是当前实现里已经存在的“避免问题持续放大”的一部分。

## 9. 当前实现的边界

这里也必须明确：

> 当前实现对 App Store 异常的规避，是通用缓存边界控制，不是 Apple 专项识别。

也就是说，当前代码已经做到的是：

1. stale TTL 很短。
2. 命中过期后立即后台刷新。
3. `SERVFAIL` 不走长 lazy 窗口。
4. fast path 不长时间缓存失败。
5. 路由变化会绕过旧缓存。
6. 运行态可直接 purge/flush。

但当前默认实现没有做到的是：

- 自动识别“这个域名属于 Apple/App Store，所以绝不直接返 stale”
- 将所有普通正响应的 fast fresh 寿命自动限制到原始 RR TTL；默认 `7200s` 与高变化域名 `43200s` 策略仍适用。
- 单凭 stale TTL 上限保证 Apple/CDN 不使用旧地址；共享 fresh 的最小 TTL 和 fast fresh 的内部寿命也会影响更新速度。

因此更准确地说：

- 当前实现阻断了 stale 被重新入库续期这一条放大路径。
- 但它属于通用保护，并不是专门为 Apple/App Store 单独硬编码的一条特殊路径。

## 10. 最终总结

如果只从“目前已经写进代码的实现”来看，当前系统避免 App Store 长时间运行异常在缓存层被长期放大，主要依靠下面几件事：

1. stale 结果对客户端只暴露极短时间，不会按 `lazy_cache_ttl` 或 `lazy_stale_ttl` 长时间下发。
2. 一旦命中过期项，就立即后台刷新，并允许后续请求快速切换到 fresh 结果。
3. `SERVFAIL` 使用独立短 TTL，不会被长 lazy 窗口放大。
4. UDP fast path 拒绝负响应及 shared stale 入库，普通正响应保留时间仍由配置控制。
5. 路由签名和依赖 revision 变化时，旧缓存会被绕过。
6. 运行态已经提供 purge/flush，不需要只能依赖重启来摆脱旧缓存。

因此，对这个问题最准确的表述应当是：

> 当前保护缩短 stale 客户端 TTL，并阻止 shared stale 经 UDP fast 再次获得 fresh 寿命；这项问题在多个旧版本也可复现，不能归为本次版本独有，更不能据此确认 Apple 故障已经解决。fresh 保留策略与真实网络链路仍需单独验证。

## 11. 参考文件

- [缓存策略真源](../config/sub_config/cache_policies.yaml)
- [缓存与上游定义](../config/sub_config/21-data-cache-upstreams.yaml)
- [Cache WAL 灰度发布与回滚手册](./CACHE_WAL_ROLLOUT.md)
- [DNS 失败类型与处理链路说明](./DNS_FAILURE_FLOW_EXPLAINED.md)
