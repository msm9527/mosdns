# Switch 命名与语义（当前版本）

> 当前版本只支持具名开关。
> 默认配置、Web UI 和运行时代码统一使用具名开关，并集中存储到 `config/rule/switches.json`。

## 当前结构

- 插件类型统一为 `switch`
- 匹配写法统一为 `switch 'name:value'`
- 状态文件统一为 `rule/switches.json`
- 不再支持 `switch1..switch15`
- 不再支持 `A/B` 取值
- 不再读取 `switch1.txt` 到 `switch15.txt`

## 具名开关

| 当前名称 | 语义 | 可用值 |
|---|---|---|
| `block_response` | 屏蔽黑名单和无结果请求 | `on` / `off` |
| `client_proxy_mode` | 客户端代理模式 | `all` / `blacklist` / `whitelist` |
| `core_mode` | 核心模式 | `compat` / `secure` |
| `branch_cache` | 分支缓存 | `on` / `off` |
| `block_query_type` | 屏蔽 SOA/PTR/HTTPS 等类型 | `on` / `off` |
| `block_ipv6` | 屏蔽 IPv6 | `on` / `off` |
| `ad_block` | 广告屏蔽 | `on` / `off` |
| `prefer_ipv4` | IPv4 优先 | `on` / `off` |
| `cn_answer_mode` | 国内域名应答模式 | `realip` / `fakeip` |
| `prefer_ipv6` | IPv6 优先 | `on` / `off` |
| `main_cache` | 主缓存总开关 | `on` / `off` |
| `udp_fast_path` | UDP 快路径 | `on` / `off` |

## 配置示例

```yaml
- tag: core_mode
  type: switch
  args:
    name: core_mode
    state_file_path: rule/switches.json
```

```yaml
- matches:
  - switch 'core_mode:secure'
  - switch 'main_cache:on'
  exec: $cache_all_noleak
```

## 接口

- 集中读取：`GET /api/v1/switches`
- 单个读取：`GET /api/v1/switches/{name}`
- 单个修改：`PUT /api/v1/switches/{name}`
- 旧的 `/plugins/switches/*` 和 `/plugins/{switch_name}` 已移除，默认 UI 已统一走核心接口，修改后实时生效
