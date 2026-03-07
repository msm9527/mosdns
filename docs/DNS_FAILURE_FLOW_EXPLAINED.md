# DNS 失败类型与处理链路说明

## 文档目的

这份文档用来回答三个容易混淆的问题：

- `SERVFAIL` 是什么
- `timeout` 是什么
- `NXDOMAIN` 是什么

以及它们在当前 mosdns 里的实际处理链路。

## 三种结果不是一回事

### 1. `SERVFAIL`

`SERVFAIL` 是 DNS 协议里的失败响应类型。

- `Rcode = 2`
- 表示“服务器处理失败”
- 这是一个明确的 DNS 响应结果

它的特点是：

- 客户端收到了响应
- 只是响应内容是失败结果

### 2. `timeout`

`timeout` 不是 DNS 响应类型，而是超时现象。

常见场景：

- 请求发出后，客户端在等待窗口内没有收到响应
- 上游在规定时间内没有返回结果
- 传输层出错，最终表现为 `i/o timeout`

它的特点是：

- 客户端可能根本没收到任何 DNS 响应包
- 所以它不是 `rcode`

### 3. `NXDOMAIN`

`NXDOMAIN` 是 DNS 协议里的正常负结果。

- `Rcode = 3`
- 表示“域名不存在”

它的特点是：

- 这是一个合法、完整、可缓存的负响应
- 不等于服务故障

## 在当前 mosdns 里分别怎么处理

### 一、正常成功结果

如果上游返回：

- `NOERROR`
- 且有可用 `A/AAAA`

当前系统会：

1. 直接返回最快的可用响应
2. 写入 `cache_*`
3. 后续请求优先命中缓存

### 二、`NXDOMAIN`

如果上游返回 `NXDOMAIN`：

1. 这会被视为“正常负结果”
2. 当前 `cache` 插件会单独使用 `nxdomain_ttl`
3. 后续请求可能直接命中负缓存

当前默认配置：

- `nxdomain_ttl: 60`

实际含义：

- 一个确定不存在的域名，不需要每次都重新打上游

### 三、`SERVFAIL`

如果上游返回 `SERVFAIL`：

1. 这会被视为“失败响应”
2. 当前 `cache` 插件会单独使用 `servfail_ttl`
3. `aliapi` 还会对热点 `SERVFAIL` 做额外失败抑制

当前默认配置：

- `servfail_ttl: 15`
- `failure_suppress_ttl: 10`
- `persistent_servfail_threshold: 3`
- `persistent_servfail_ttl: 60`

实际含义：

- 一次 `SERVFAIL` 不会被长期缓存
- 但固定热点失败域名不会反复穿透上游

### 四、`timeout`

如果上游在等待窗口内没有返回：

1. 它首先表现为 transport error / `i/o timeout`
2. `aliapi` 会继续等待其他并发上游
3. 如果所有选中的并发上游都 timeout / transport fail：
   - 当前实现会合成一个短期 `SERVFAIL`
4. 这个短期 `SERVFAIL` 后续会进入失败抑制和短期缓存链路

这意味着：

- `timeout` 本身不是 DNS 响应类型
- 但在当前实现里，连续或全量超时最终可能被收敛成 `SERVFAIL`

## 为什么“超过 5 秒”不等于 `SERVFAIL`

这两者属于不同层级：

- `SERVFAIL`
  - 协议层结果
  - 有响应包
- `timeout`
  - 传输或等待层现象
  - 可能没响应包

所以：

- “超过 5 秒”本身不叫 `SERVFAIL`
- 只是当前系统可能把“所有上游都超时”的情况，最终收敛成一个 `SERVFAIL` 给客户端

## 当前链路里的实际收敛流程

可以简化理解成这样：

```text
请求进入
  -> 并发查询多个上游
    -> 任一上游快速返回可用 A/AAAA
       -> 直接成功返回
    -> 上游返回 NXDOMAIN
       -> 作为正常负结果返回并负缓存
    -> 上游返回 SERVFAIL
       -> 作为失败结果返回并短期失败缓存
    -> 所有上游都 timeout / transport fail
       -> 系统合成短期 SERVFAIL
       -> 进入失败抑制链路
```

## 为什么压测里会看到“缓存阶段还有失败”

这不一定代表缓存失效。

常见原因是：

### 1. 负结果被正常复用

例如：

- `NXDOMAIN`
- `SERVFAIL`

它们也可能命中缓存，所以“缓存阶段还有失败”是正常现象。

### 2. 热点坏域名被快速复用

例如：

- `114menhu.com`
- `alimei.com`

这类域名本身长期 `SERVFAIL`，现在系统会优先做失败抑制，避免反复回源。

所以它表现为：

- 失败仍然存在
- 但延迟更低
- 对上游压力更小

## 当前版本的专业处理原则

当前系统的处理原则是：

- `NOERROR`：正常缓存
- `NXDOMAIN`：当作正常负结果缓存
- `SERVFAIL`：短期失败缓存
- `timeout/transport_error`：不长期缓存，但必要时收敛成短期 `SERVFAIL`
- 固定热点 `SERVFAIL`：提升为更长的失败抑制窗口

这套策略的目的不是“把所有失败都变成功”，而是：

- 不让热点坏域名持续拖垮系统
- 不让瞬时故障长期固化
- 不把正常负结果误判成系统故障

## 对运维判断的建议

遇到失败时，先分清是哪一类：

- `NXDOMAIN`
  - 先看是不是域名本来就不存在
- `SERVFAIL`
  - 先看是不是权威或公共解析都失败
- `timeout`
  - 先查上游连通性、超时和熔断

不要把这三类问题混成“DNS 服务坏了”。

## 相关配置和代码位置

配置：

- [cache.yaml](./../config/sub_config/cache.yaml)
- [forward_local.yaml](./../config/sub_config/forward_local.yaml)
- [forward_nocn.yaml](./../config/sub_config/forward_nocn.yaml)
- [forward_nocn_ecs.yaml](./../config/sub_config/forward_nocn_ecs.yaml)

代码：

- [cache.go](./../plugin/executable/cache/cache.go)
- [aliapi.go](./../plugin/executable/aliapi/aliapi.go)

配套文档：

- [DNS_FAILURE_GOVERNANCE_PLAN.md](./DNS_FAILURE_GOVERNANCE_PLAN.md)
