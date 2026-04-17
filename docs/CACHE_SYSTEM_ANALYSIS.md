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
- 对旁路 stale refresh 做一个很短的热点缓冲。

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

- 真实 `msgTtl` 最多裁剪到 `300s`
- `cacheTtl` 仍可能被 `lazy_cache_ttl` 拉长

### `NXDOMAIN`

- 使用 `nxdomain_ttl`

### `SERVFAIL`

- 使用 `servfail_ttl`

### 最小下限

如果算出来的 TTL 小于等于零，会被抬到：

- `5s`

## 4.8 lazy stale 与 lazy refresh

当前实现里的 lazy stale 有两个层面：

1. 后台保留窗口
   - 由 `lazy_cache_ttl` 决定。
2. 返回给客户端的 stale TTL
   - 固定为 `5s`

也就是说：

- `lazy_cache_ttl` 决定 stale 记录还能在缓存里活多久。
- 客户端看到的 TTL 很短，目的是促使后续尽快重新请求。

当普通域名首次命中过期缓存时，当前行为通常是：

1. 先返回 stale。
2. 启动后台刷新。
3. 后续短时间内的再次请求，有机会拿到 fresh 结果。

此外还有 prefetch：

- 对接近过期的热点项，缓存会提前触发后台刷新。

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

当前 WAL 只记录：

- `store`

这意味着：

- 新写入/覆盖会追加 WAL。
- `delete` / `flush` / `purge` 没有独立 delete record。
- 删除类操作主要依赖后续 dump/checkpoint 固化。

### 恢复顺序

重启恢复时：

1. 先读 snapshot。
2. 再回放 WAL。

### dump 触发特点

周期 dump 不是每次到点都写，而是：

- 到了 `dump_interval`
- 且 `updatedKey >= 1024`
- 才真正触发 dump

这是一种减少频繁落盘的策略。

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

### 5.1 缓存对象

快路径缓存保存的是：

- 预裁剪 TTL 后的二进制响应包

### 5.2 缓存范围

当前只缓存：

- `NOERROR`
- `NXDOMAIN`

不会缓存：

- `SERVFAIL`
- 非法响应

### 5.3 过期语义

默认策略较短：

- `internal_ttl = 5s`
- `stale_retry_seconds = 10s`
- `ttl_min = 1`
- `ttl_max = 5`

因此它更像是：

- 极短时热点缓冲
- 短窗口 stale-refresh 辅助层

它不是长时间运行后异常积累的主要来源。

## 6. 当前设计的几个关键特征

可以把当前缓存系统概括成下面几条：

1. 响应缓存是“分层 + stale + 持久化 + runtime control”的完整系统，不是普通 map cache。
2. 路由变化和依赖 revision 已经被纳入缓存失效条件，不只是 TTL 到期才失效。
3. 持久化默认开启后，缓存行为会跨重启延续。
4. UDP 快路径缓存是独立短缓存，不应和响应缓存混为一谈。
5. 当前 L2 的真实淘汰语义不是严格 LRU，这一点需要在设计说明里明确。

---

## Part II. 当前方案如何避免 App Store 长时间运行异常

## 7. 问题本质

`Apple App Store 长时间运行后网络异常` 这类问题，本质上通常不是“域名永久失效”，而是：

1. Apple / App Store / CDN 相关域名变化快。
2. 某些解析结果真实 TTL 很短。
3. 如果 DNS 层仍把过期结果继续提供给客户端，即使只给 `5s`，客户端第一次访问也可能命中 stale 地址。
4. 后台刷新回来后，后续再点刷新又恢复。

所以它本质是：

> 高变更域名与 stale-serve 策略之间的冲突。

## 8. 当前设计里的规避路径

当前仓库里，这类问题的规避不是靠单一开关，而是靠一条完整路径。

## 8.1 第一步：把 Apple/App Store 域名单独分组

这类问题要规避，前提不是 `DDNS`，而是：

> 让 Apple/App Store 这批高变更域名不要继续使用“通用域名的一刀切缓存策略”。

设计上最合理的做法是：

1. 在规则层把 Apple/App Store 域名单独归组。
2. 在 sequence 层给这组流量挂单独缓存策略。

接入点通常仍然是：

- [config/sub_config/20-data-sources.yaml](../config/sub_config/20-data-sources.yaml)

但这里的重点不是复用某个既有标签名，而是：

- 要有一组单独可识别的 Apple/App Store 域名分类。
- 这组分类后面要能接到单独的 cache 行为。

## 8.2 第二步：给这组域名使用保守缓存策略

这是规避 App Store 长时间运行异常最关键的部分。

普通域名过期后，当前缓存默认仍可能：

1. 先回 stale。
2. 再后台刷新。

但 Apple/App Store 这类域名不适合继续吃这套默认路径。更保守的方案应当是：

1. 给它们单独走一个 response cache。
2. 这个 cache 设置更短的 stale 窗口，或者直接 `lazy_cache_ttl: 0`。
3. 必要时进一步缩短 `servfail_ttl`，避免临时故障被放大。

这一步规避的正是：

- 第一次访问命中过期 CDN IP
- 然后客户端表现成“网络异常”

## 8.3 第三步：利用 prefetch 和短 TTL 减少真正撞上 stale 的概率

当前设计里，本来就存在 prefetch：

- 对接近过期的热点项，缓存会提前触发后台刷新。

如果 Apple/App Store 走了单独的保守 cache，再配合：

- 更短的 `lazy_cache_ttl`
- 更短的真实缓存寿命

就可以进一步减少客户端真正撞上 stale 的概率。

## 8.4 第四步：路由变化或依赖变化时，直接绕过旧缓存

App Store 异常不一定只来自 TTL 过期，也可能来自：

- 分流策略变化
- 列表 revision 变化
- 依赖数据更新后，旧缓存不该再被用

当前设计里，`route_signature` 机制会在这些场景下直接绕过旧缓存。

这避免的是另一类“明明规则变了，但 DNS 还在吃旧答案”的问题。

## 8.5 第五步：必要时运行态精确 purge

如果发现某一批 Apple 域名已经出现异常，不需要等它自然过期。

当前设计支持：

- 按域名 purge 响应缓存
- 按域名 purge UDP fast cache
- 全量 flush

这一步的意义是：

- 出现故障时，不必通过重启进程来绕过旧缓存。

## 8.6 第六步：必要时拆出专用 cache

如果一批域名对 stale 特别敏感，当前设计允许继续收敛为更保守的方案：

1. 给它们单独走一个 sequence 分支。
2. 挂单独的 `type: cache`。
3. 这个 cache 设置为：
   - `lazy_cache_ttl: 0`
   - 可选 `persist: false`

这意味着：

- 同一个系统里，大多数域名仍可保留 lazy cache 的收益。
- Apple/App Store 这一类域名可以使用保守缓存策略。

## 9. 这套规避方案真正生效的前提

这里有一个必须明确写出来的前提：

> 只有当 Apple/App Store 相关域名被单独分组，并绑定到保守缓存策略时，上面那条规避链才会真正生效。

在当前仓库默认规则下：

- Apple 相关域名主要在 [whitelist.txt](../config/rule/whitelist.txt)。
- 它们默认仍然走通用真实解析缓存策略。

这意味着当前默认状态是：

- 它们会走白名单/直连逻辑。
- 但不会自动获得“Apple 专用保守缓存策略”。

所以准确地说：

- 当前设计已经具备规避这类问题的能力。
- 但要让它落到 Apple/App Store 上，还需要单独分组并绑定对应 cache。

## 10. 在当前仓库里的推荐落地方式

如果目标是让这套设计真正覆盖 App Store 异常，建议按优先级这样落地。

## 10.1 首选：给 Apple/App Store 域名单独分组，并绑定专用保守 cache

这是最符合当前设计边界的做法。

好处：

- 不需要重写缓存内核。
- 只对 Apple/App Store 这组域名收紧策略。
- 影响范围可控。

建议先精确收口真实故障域名，例如：

- `apps.apple.com`
- `itunes.apple.com`
- `buy.itunes.apple.com`
- `bag.itunes.apple.com`
- `ppq.apple.com`
- `push.apple.com`
- `push-apple.com.akadns.net`
- `lcdn-locator.apple.com`
- `lcdn-registration.apple.com`
- `cn-ssl.ls.apple.com`
- `mzstatic.com`
- `aaplimg.com`

推荐配置方向：

1. Apple 域名单独分支。
2. 挂单独响应缓存。
3. 使用更保守策略：
   - `lazy_cache_ttl: 0`
   - 更短的 `servfail_ttl`
   - 可选关闭持久化

## 10.2 次选：整体收紧真实解析缓存

如果不想拆分规则，也可以全局保守化：

- 把 `cache_main/cache_branch_*` 的 `lazy_cache_ttl` 压低
- 把 `servfail_ttl` 压低

这会降低 stale 风险，但代价是：

- 整体缓存收益下降
- 上游查询频率会上升

## 11. 可以直接用于故障止血的接口

### 按域名清理

```bash
curl -X POST http://127.0.0.1:9099/api/v1/cache/purge_domains \
  -H 'Content-Type: application/json' \
  -d '{
    "tags": ["cache_main", "cache_branch_domestic", "cache_branch_foreign", "cache_branch_foreign_ecs"],
    "domains": ["apps.apple.com", "itunes.apple.com", "push.apple.com"],
    "qtypes": [1, 28]
  }'
```

### 全量清理

```bash
curl -X POST http://127.0.0.1:9099/api/v1/cache/flush_all \
  -H 'Content-Type: application/json' \
  -d '{
    "include_udp_fast": true
  }'
```

## 12. 最终总结

这套缓存设计规避 App Store 长时间运行异常的核心思想，其实很简单：

1. 不把所有域名一视同仁。
2. 把 Apple/App Store 相关域名单独分组。
3. 这组域名使用更保守的缓存策略，不继续走默认 stale 路径。
4. 必要时由 route signature、runtime purge 和专用 cache 继续兜底。

因此，对这个问题最准确的描述是：

> 当前仓库的缓存设计已经有能力规避这类问题；真正要让它落到 Apple/App Store 上，需要把对应域名单独分组，并绑定保守缓存策略。

## 13. 参考文件

- [缓存策略真源](../config/sub_config/cache_policies.yaml)
- [缓存与上游定义](../config/sub_config/21-data-cache-upstreams.yaml)
- [统一标签映射](../config/sub_config/20-data-sources.yaml)
- [Apple 当前规则清单](../config/rule/whitelist.txt)
- [Cache WAL 灰度发布与回滚手册](./CACHE_WAL_ROLLOUT.md)
- [DNS 失败类型与处理链路说明](./DNS_FAILURE_FLOW_EXPLAINED.md)
