# Rewrite 规则说明

本文档说明 `rewrite` 插件在当前仓库中的规则格式、运行行为和默认链路位置。

## 1. 规则格式

每行一条规则，固定两列：

```text
<匹配规则> <目标IP或目标域名>
```

示例：

```text
example.com 1.2.3.4
example.com 1.2.3.5
example.com example.net
full:api.example.com example.net
domain:example.org 2001:db8::1
```

说明：

- 第一列是匹配规则，支持普通域名，也支持 `full:`、`domain:` 等域名匹配写法。
- 第二列可以是目标 IP，也可以是目标域名。
- 规则文件位置默认在 [`config/sub_config/20-data-sources.yaml`](../config/sub_config/20-data-sources.yaml) 中配置，当前默认读取 `rule/rewrite.txt`。

## 2. 多目标写法

单个域名如果需要配置多个重定向目标，不是写成一行多列，而是重复写多行：

```text
example.com 1.2.3.4
example.com 1.2.3.5
example.com example.net
```

当前实现会按同一个匹配规则聚合目标，并在运行时自动去重。

## 3. 实际行为

- 仅对 `A` 和 `AAAA` 查询生效。
- `TXT`、`MX`、`HTTPS` 等其他查询类型不会被 `rewrite` 吞掉，而是继续走后续解析链路。
- 当目标是 IP 时，直接返回对应的 `A` 或 `AAAA` 记录；如果目标 IP 类型和查询类型不一致，例如只配置 IPv4 但收到 `AAAA` 查询，会返回空的 `NOERROR/NODATA` 响应并停止后续解析。
- 当目标是域名时，插件会向 `rewrite.args.dns` 指定的上游发起查询，并把结果展开为最终的 `A` 或 `AAAA` 记录返回。
- 如果目标域名返回 `CNAME`，当前实现会继续跟随 `CNAME` 链并展开，默认最多跟随 8 跳。
- 返回结果的 TTL 当前固定为 5 秒。

## 4. 与缓存的关系

按当前默认配置，`rewrite` 位于主入口分流之前、真实解析缓存链之前：

```text
sequence_common_precheck -> top_domains -> rewrite -> has_resp exit -> sequence_ipv4/sequence_ipv6/sequence_other
```

这意味着：

- 命中 `rewrite` 后，会直接生成响应并返回；包括静态 IP 类型不匹配时生成的空响应。
- 默认不会进入 `main_cache` 或 `branch_cache`。
- 如果目标写成域名，是否命中缓存取决于 `rewrite.args.dns` 指向的那个上游服务本身有没有缓存。
- 面向 Stash、Clash.Meta、sing-box 的适配入口会保留 `rewrite` 结果，不再把重定向 IP 二次改写为 fake/local 响应。

## 5. 默认配置位置

当前仓库默认配置中的 `rewrite` 插件定义在：

- [`config/sub_config/20-data-sources.yaml`](../config/sub_config/20-data-sources.yaml)

默认配置片段：

```yaml
- name: rewrite
  type: rewrite
  args:
    files:
      - rule/rewrite.txt
    dns: 127.0.0.1:2222
```
