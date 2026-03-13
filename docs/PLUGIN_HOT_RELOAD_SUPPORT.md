# 插件热重载支持矩阵

本文档说明当前仓库里哪些插件支持 `RuntimeConfigReloader` 热重载，哪些不支持，以及这些插件对应的配置文件位置。

完整插件类型清单见 [docs/PLUGIN_TYPE_MATRIX.md](./PLUGIN_TYPE_MATRIX.md)。本文档重点回答两件事：

1. 哪些插件改配置后可以不重启进程直接生效。
2. 这些插件在当前仓库里是从哪个配置文件加载的。

## 判断标准

当前代码里，插件是否支持“配置热重载”，统一以是否实现 `coremain.RuntimeConfigReloader` 为准：

- 接口定义：`coremain/runtime_reload.go`
- 当前实现该接口的插件有 11 个：
  - `forward`
  - `aliapi`
  - `sequence`
  - `ip_set`
  - `domain_set`
  - `domain_set_light`
  - `sd_set_light`
  - `si_set`
  - `rewrite`
  - `adguard_rule`
  - `domain_mapper`

也就是说，`保存并生效` 最终是否能不重启进程完成，取决于改动最终命中的插件是否在上面这 3 个里。

## 范围说明

- “默认配置”指 `config/config.yaml` 及其 `include` 的 `config/sub_config/*.yaml`
- `config/examples/*.yaml` 只算示例，不会被默认启动流程加载
- 某些插件虽然不实现 `RuntimeConfigReloader`，但有自己的运行时 API，例如：
  - `switch`：通过 `/api/v1/switches/*` 即时修改状态
  - `requery`：通过 `/plugins/requery/*` 即时触发、取消、改调度

这类插件属于“有运行时操作”，但不属于“通过配置热重载重建插件实例”。

## 一览

- 源码中当前注册的插件类型：32 个
- 当前默认配置里实际用到的插件类型：18 个
- 支持 `RuntimeConfigReloader` 热重载的插件类型：11 个
- 当前默认配置里实际用到且支持热重载的插件类型：10 个

## 支持热重载的插件

| 插件类型 | 是否默认配置使用 | 源码文件 | 对应配置文件 | 说明 |
| --- | --- | --- | --- | --- |
| `aliapi` | 是 | `plugin/executable/aliapi/aliapi.go` | `config/sub_config/forward_local.yaml` `config/sub_config/forward_nocn.yaml` `config/sub_config/forward_nocn_ecs.yaml` `config/sub_config/forward_1.yaml` | 上游 DNS 配置、全局 `SOCKS5`、`ECS IP` 保存并生效时会命中这里 |
| `adguard_rule` | 是 | `plugin/executable/adguard/adguard.go` | `config/sub_config/adguard.yaml` | 规则目录和 `socks5` 覆盖现在可以运行时重载 |
| `domain_mapper` | 是 | `plugin/data_provider/domain_mapper/domain_mapper.go` | `config/sub_config/rule_set.yaml` | 聚合规则映射器现在支持 provider 重绑定和运行时重建 |
| `domain_set` | 是 | `plugin/data_provider/domain_set/domain_set.go` | `config/sub_config/rule_set.yaml` | 规则文件、表达式、关联集合现在支持运行时重建 |
| `domain_set_light` | 是 | `plugin/data_provider/domain_set_light/domain_set_light.go` | `config/sub_config/rule_set.yaml` | 轻量规则集改动现在可热应用 |
| `sequence` | 是 | `plugin/executable/sequence/sequence.go` | `config/config.yaml` `config/sub_config/process_v4.yaml` `config/sub_config/process_v6.yaml` `config/sub_config/process_ot.yaml` `config/sub_config/not_in_list_ipmatch.yaml` `config/sub_config/not_in_list_leak_v4.yaml` `config/sub_config/not_in_list_leak_v6.yaml` `config/sub_config/not_in_list_noleak_v4.yaml` `config/sub_config/not_in_list_noleak_v6.yaml` `config/sub_config/forward_nocn.yaml` `config/sub_config/forward_nocn_ecs.yaml` `config/sub_config/forward_1.yaml` `config/sub_config/requery_refresh.yaml` `config/sub_config/for_singbox.yaml` | 当前全局 `ECS IP` 热生效，核心就是这里也支持运行时重建 |
| `ip_set` | 是 | `plugin/data_provider/ip_set/ip_set.go` | `config/sub_config/rule_set.yaml` | IP 集合配置支持运行时重建 |
| `rewrite` | 是 | `plugin/executable/rewrite/rewrite.go` | `config/sub_config/rule_set.yaml` | 重写规则和上游 DNS 地址支持热重载 |
| `sd_set_light` | 是 | `plugin/data_provider/sd_set_light/sd_set_light.go` | `config/sub_config/rule_set.yaml` | 规则源配置文件和 `socks5` 覆盖支持热重载 |
| `si_set` | 是 | `plugin/data_provider/si_set/si_set.go` | `config/sub_config/rule_set.yaml` | 规则源配置文件和 `socks5` 覆盖支持热重载 |
| `forward` | 否 | `plugin/executable/forward/forward.go` | `config/examples/forward_2.yaml` | 代码支持热重载，但当前默认配置没有启用，只在示例配置里出现 |

## 默认配置中使用但不支持 `RuntimeConfigReloader` 的插件

下表这些插件在 `config/config.yaml` 的默认加载链路里会被创建，但它们本身不实现 `RuntimeConfigReloader`。如果直接改它们的 YAML 定义，通常仍然需要重启进程才能完整生效。

| 插件类型 | 源码文件 | 对应配置文件 | 备注 |
| --- | --- | --- | --- |
| `cache` | `plugin/executable/cache/cache.go` | `config/sub_config/cache.yaml` | 缓存清空可即时执行，但缓存插件配置本身不支持热重载 |
| `domain_output` | `plugin/executable/domain_output/domain_output.go` | `config/sub_config/domain_output.yaml` | 配置改动不支持运行时重建 |
| `fallback` | `plugin/executable/sequence/fallback/fallback.go` | `config/sub_config/for_singbox.yaml` | 当前未实现热重载接口 |
| `requery` | `plugin/executable/requery/requery.go` | `config/sub_config/requery.yaml` | 调度和触发有运行时 API，但 YAML 配置本身不走 `RuntimeConfigReloader` |
| `switch` | `plugin/switch/switch/switch.go` | `config/sub_config/switch.yaml` | 开关值通过 `/api/v1/switches/*` 即时生效，但插件定义本身不支持热重载 |
| `tcp_server` | `plugin/server/tcp_server/tcp_server.go` | `config/config.yaml` `config/sub_config/forward_nocn.yaml` `config/sub_config/forward_nocn_ecs.yaml` `config/sub_config/forward_1.yaml` `config/sub_config/requery_refresh.yaml` `config/sub_config/for_singbox.yaml` | 监听地址、入口链路等配置改动通常要重启 |
| `udp_server` | `plugin/server/udp_server/udp_server.go` | `config/config.yaml` `config/sub_config/forward_nocn.yaml` `config/sub_config/forward_nocn_ecs.yaml` `config/sub_config/forward_1.yaml` `config/sub_config/requery_refresh.yaml` `config/sub_config/for_singbox.yaml` | 同上 |
| `webinfo` | `plugin/executable/webinfo/webinfo.go` | `config/sub_config/webinfo.yaml` | 主要用于前端信息与配置存储，当前无热重载接口 |

## 代码里已注册但默认配置未启用，且当前不支持热重载的插件

下列插件在源码里已注册，但默认 `config/config.yaml` 并不会加载它们。它们当前也没有实现 `RuntimeConfigReloader`。

| 插件类型 | 源码文件 |
| --- | --- |
| `arbitrary` | `plugin/executable/arbitrary/arbitrary.go` |
| `cname_remover` | `plugin/executable/cname_remover/cname_remover.go` |
| `ecs_handler` | `plugin/executable/ecs_handler/handler.go` |
| `fast_mark` | `plugin/matcher/fast_mark/fast_mark.go` |
| `hosts` | `plugin/executable/hosts/hosts.go` |
| `http_server` | `plugin/server/http_server/http_server.go` |
| `quic_server` | `plugin/server/quic_server/quic_server.go` |
| `rate_limiter` | `plugin/executable/rate_limiter/rate_limiter.go` |
| `redirect` | `plugin/executable/redirect/redirect.go` |
| `reverse_lookup` | `plugin/executable/reverse_lookup/reverse_lookup.go` |
| `sd_set` | `plugin/data_provider/sd_set/sd_set.go` |
| `sleep` | `plugin/executable/sleep/sleep.go` |
| `tag_setter` | `plugin/executable/tag_setter/tag_setter.go` |

## 现在可以直接按什么原则判断

如果你在 UI 或接口里修改的是下面这些内容，当前可以按这个规则判断是否需要重启：

- 改 `上游 DNS`、全局 `SOCKS5`、全局 `ECS IP`
  - 只要最终命中的是 `aliapi` / `forward` / `sequence`
  - 可以走 `保存并生效`
  - 不需要重启进程
- 改 `rule_set.yaml`、`adguard.yaml` 这类规则/数据插件配置
  - 如果命中的是 `ip_set` / `domain_set` / `domain_set_light` / `sd_set_light` / `si_set` / `rewrite` / `adguard_rule` / `domain_mapper`
  - 当前也可以走 `保存并生效`
  - 不需要重启进程
- 改 `switch` 开关
  - 通过插件运行时 API 即时生效
  - 不需要重启进程
- 改 `requery` 的触发、取消、调度
  - 通过插件运行时 API 即时生效
  - 不需要重启进程
- 直接改其他插件的 YAML 定义
  - 如果该插件不在本文“支持热重载的插件”表里
  - 原则上按“需要重启”处理更安全

## 后续维护建议

以后如果新增某个插件支持热重载，需要同时更新两处：

1. 插件实现 `coremain.RuntimeConfigReloader`
2. 本文档的支持矩阵

否则 UI 上“保存并生效”和实际行为会继续出现偏差。
