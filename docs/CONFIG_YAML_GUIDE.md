# 配置总览与维护手册（YAML）

本文档面向日常维护者，解释 `config/config.yaml` 与 `config/sub_config/*.yaml` 的职责、依赖顺序、关键标记语义与修改注意事项。

补充说明：

- 用户日常要改的 `socks5 / ecs / 上游 DNS 列表`，优先修改 `config/custom_config/*.yaml`
- 用户日常要改的 `功能开关`，也优先修改 `config/custom_config/switches.yaml`
- `config/sub_config/*.yaml` 里的对应项现在更偏向“基线配置”和结构定义
- 前端控制面保存也会写入 `config/custom_config/*.yaml`

## 1. 主入口与 include 顺序

主配置文件：`config/config.yaml`

核心原则：

1. `include` 顺序不要随意改。后面的子配置会引用前面定义的 tag。
2. 分流主入口是 `sequence_6666`，刷新入口是 `sequence_requery` 与 `sequence_requery_refresh`。
3. 共享前置逻辑集中在 `sequence_common_precheck`，避免主链路和刷新链路漂移。

## 2. 请求处理主路径

实时请求路径（53 端口）：

1. `udp_all/tcp_all` 接入。
2. 进入 `sequence_6666`。
3. 执行 `sequence_common_precheck`：
   - qtype 拦截（SOA/PTR/HTTPS、可选 AAAA）
   - `unified_matcher1` 打 fast_mark
   - 黑名单/无 A/无 AAAA/广告拦截
   - 指定 client_ip 直连判定
   - prefer_ipv4/prefer_ipv6
   - 主缓存总开关（`main_cache:on` 时启用 `cache_all_noleak`）
4. 按 qtype 分流到：
   - A: `sequence_ipv4`
   - AAAA: `sequence_ipv6`
   - 其他: `sequence_other`

刷新请求路径（7766 / 7767 端口）：

1. `sequence_requery` 与 `sequence_requery_refresh` 复用主逻辑语义。
2. 刷新链路不写 top_domains，避免污染访问排行。

## 3. 子配置文件职责（逐文件）

| 文件 | 主要职责 | 被谁引用 |
|---|---|---|
| `sub_config/10-control.yaml` | 控制面对象注册（switch / webinfo / requery） | 主链路、UI、control API |
| `sub_config/20-data-sources.yaml` | 规则源、动态规则生成、统一匹配器 | 主链路与刷新链路 |
| `sub_config/21-data-cache-upstreams.yaml` | cache、国内/国外上游、fakeip 相关基础序列 | 主链路与刷新链路 |
| `sub_config/31-main-resolution.yaml` | 主链路基础解析 helper | `33`、`34` |
| `sub_config/32-main-not-in-list.yaml` | 主链路列表外 IP 二次判断 | `33-main-ipv4v6.yaml` |
| `sub_config/33-main-ipv4v6.yaml` | 主链路 A / AAAA 核心分流 | `34-main-entry.yaml` |
| `sub_config/34-main-entry.yaml` | 主入口、主缓存、stash/clashmeta/sing-box 入口 | listeners |
| `sub_config/40-refresh-resolution.yaml` | 刷新链基础解析 helper | `42` |
| `sub_config/41-refresh-not-in-list.yaml` | 刷新链列表外 IP 二次判断 | `42-refresh-ipv4v6.yaml` |
| `sub_config/42-refresh-ipv4v6.yaml` | 刷新链 A / AAAA 核心分流 | requery |
| `sub_config/50-listeners.yaml` | 所有对外监听端口与入口序列 | 最终装配 |

## 4. fast_mark 对照（配置侧）

来源：`sub_config/rule_set.yaml` 的 `unified_matcher1` 与 `process_*` / `not_in_list_*`。

| fast_mark | 语义 | 主要产生位置 |
|---|---|---|
| 1 | 黑名单 | `unified_matcher1` |
| 2 | 记忆无 V4 | `unified_matcher1` |
| 3 | 记忆无 V6 | `unified_matcher1` |
| 5 | 广告 | `unified_matcher1` |
| 6 | DDNS | `unified_matcher1` |
| 7 | 灰名单 | `unified_matcher1` |
| 8 | 白名单 | `unified_matcher1` |
| 9 | !CN fakeip filter | `unified_matcher1` |
| 10 | CN fakeip filter | `unified_matcher1` |
| 11 | 记忆直连 | `unified_matcher1` |
| 12 | 记忆代理 | `unified_matcher1` |
| 13 | 订阅直连补充 | `unified_matcher1` |
| 14 | 订阅代理 | `unified_matcher1` |
| 15 | 订阅代理补充 | `unified_matcher1` |
| 16 | 订阅直连 | `unified_matcher1` |
| 17 | 默认未命中 | `unified_matcher1.default_mark` |
| 18 | 命中直连 IP | `process_v4/v6` |
| 19 | IPv4 有效无污染 | `process_v4` |
| 20 | IPv6 无有效结果/污染 | `process_v6` |
| 21 | IPv6 有效无污染 | `process_v6` |
| 22 | leak-v4 首查失败/污染 | `not_in_list_leak_v4` |
| 23 | leak-v6 无 IPv6 | `not_in_list_leak_v6` |
| 24 | 列表外命中 direct_ip | `not_in_list_ipmatch` |
| 25 | 列表外命中 !geoip_cn | `not_in_list_ipmatch` |
| 26 | clashmi 国外标记 | `config.yaml` |
| 27 | noleak-v4 无 IPv4 | `not_in_list_noleak_v4` |
| 28 | noleak-v6 无 IPv6 | `not_in_list_noleak_v6` |
| 39 | 指定客户端直连 | `sequence_common_precheck` / refresh |
| 48 | 快路径已做 client_ip 判定 | UDP 快路径（代码侧） |

## 5. 端口清单（默认）

| 端口 | 用途 |
|---|---|
| `53` | 主服务入口（udp_all/tcp_all） |
| `7766` | requery 主刷新入口 |
| `7767` | requery_refresh 专用链路 |
| `7722` | 国内链路调试入口 |
| `7733` | 国外链路调试入口 |
| `7744` | 国外 ECS 链路调试入口 |
| `7799` | clashmi 专用入口 |
| `8888` | sing-box 常规入口 |
| `9999` | 节点域名解析入口 |
| `9099` | HTTP API / Web UI |

## 6. 修改检查清单

每次改 YAML，建议按以下顺序检查：

1. include 顺序是否被破坏。
2. tag 名称是否与引用处一致。
3. fast_mark 编号是否与现有语义冲突。
4. 主链路与刷新链路是否保持语义一致。
5. `config/custom_config/switches.yaml` 的开关取值是否与预期一致。
6. 启动校验：`./bin/mosdns start -c config/config.yaml`。

## 7. 相关文档

- `docs/DNS_FLOW_DETAILED.md`
- `docs/DNS_FLOW_VISUAL_ORDER.md`
- `docs/SWITCH_1_15_FUNCTIONS.md`
- `docs/STRESS_TEST_GUIDE.md`
