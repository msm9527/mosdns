# Switch1-15 功能对照（当前版本）

> 范围：`switch1` 到 `switch15`  
> 配置来源：`config/sub_config/switch.yaml` + `config/config.yaml` + `config/sub_config/*.yaml` + Web UI 文案

## 当前值（读取自 `config/rule/switchX.txt`）

| 开关 | 当前值 |
|---|---|
| switch1 | A |
| switch2 | B |
| switch3 | B |
| switch4 | A |
| switch5 | A |
| switch6 | B |
| switch7 | B |
| switch8 | B |
| switch9 | A |
| switch10 | B |
| switch11 | A |
| switch12 | B |
| switch13 | A |
| switch14 | A |
| switch15 | A |

## 功能映射

| 开关 | 主要功能 | A / B 语义 | 主要生效位置 |
|---|---|---|---|
| switch1 | 请求屏蔽（黑名单、无 A、无 AAAA） | A=启用，B=关闭 | `sequence_6666`、`sequence_requery` 里基于 `fast_mark 1/2/3` 拒绝 |
| switch2 | 指定客户端 fakeip 策略（配合 switch12） | A=启用该分支，B=关闭该分支 | `sequence_6666`、`sequence_requery` 中与 `client_ip` 联动，触发 `fast_mark 39` |
| switch3 | 核心运行模式（兼容/安全） | A=兼容(泄露)，B=安全(不泄露) | 选择 `cache_all` / `cache_all_noleak`，并影响列表外域名处理分支 |
| switch4 | 过期缓存 1（国内/国外分支缓存） | A=启用，B=关闭 | `forward_1.yaml`、`forward_nocn.yaml` 等序列中的 `cache_cn/cache_google/...` |
| switch5 | 类型屏蔽（SOA/PTR/HTTPS） | A=启用，B=关闭 | `sequence_6666`、`sequence_requery` 起始阶段 `qtype 6 12 65` 拒绝 |
| switch6 | AAAA 屏蔽 | A=启用，B=关闭 | `sequence_6666`、`sequence_requery` 起始阶段 `qtype 28` 拒绝 |
| switch7 | 广告屏蔽 | A=启用，B=关闭 | `unified_matcher1` 命中 `fast_mark 5` 后在主序列拒绝 |
| switch8 | IPv4 优先 | A=启用，B=关闭 | `sequence_6666`、`sequence_requery` 里触发 `prefer_ipv4` |
| switch9 | CNFakeIP 切换 | A=国内域名走 realip，B=国内域名走 fakeip | `forward_1.yaml` 的 `sequence_local_divert` |
| switch10 | IPv6 优先 | A=启用，B=关闭 | `sequence_6666`、`sequence_requery` 里触发 `prefer_ipv6` |
| switch11 | 阿里私有 DOH（预留） | A=开，B=关 | 当前默认主链路未引用（仅定义了开关插件） |
| switch12 | 指定客户端 realip 策略（配合 switch2） | A=启用该分支，B=关闭该分支 | `sequence_6666`、`sequence_requery` 中与 `client_ip` 联动，触发 `fast_mark 39` |
| switch13 | 过期缓存 2（全局缓存总开关） | A=启用，B=关闭 | 决定是否执行 `cache_all` / `cache_all_noleak` |
| switch14 | 运营商 DNS（预留） | A=开，B=关 | 当前默认主链路未引用（仅定义了开关插件） |
| switch15 | 极限加速（UDP 快路径） | A=启用，B=关闭 | `udp_server` 快路径旁路与缓存（仅 UDP 服务） |

## 使用说明

- 查询单个开关状态：`GET /plugins/switchX/show`
- 动态切换开关：`POST /plugins/switchX/post`，请求体示例：`{"value":"A"}`
- 建议联动关系：
  - `switch2` 与 `switch12` 一般只启一个方向，避免策略冲突。
  - `switch8` 与 `switch10` 建议互斥（一个开一个关）。
  - `switch15` 只影响 `udp_server`，不影响 TCP/DoH/DoQ 监听器。
