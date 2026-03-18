# 功能开关语义（当前版本）

> 当前版本只支持具名开关。  
> 所有长期值统一存放在 `config/custom_config/switches.yaml`。

## 当前结构

- 插件类型统一为 `switch`
- 匹配写法统一为 `switch 'name:value'`
- 前端 API、运行态插件和 YAML 真源保持同一套名字
- 不再支持 `switch1..switch15`
- 不再支持旧的 `A/B` 取值语义

## 具名开关

| 当前名称 | 语义 | 可用值 |
|---|---|---|
| `block_response` | 屏蔽黑名单和无结果请求 | `on` / `off` |
| `client_proxy_mode` | 客户端代理模式 | `all` / `blacklist` / `whitelist` |
| `main_cache` | 真实解析主缓存 | `on` / `off` |
| `branch_cache` | 真实解析分支缓存（国内/国外/ECS） | `on` / `off` |
| `fakeip_cache` | fakeip 响应缓存 | `on` / `off` |
| `probe_cache` | 节点探测专用缓存 | `on` / `off` |
| `block_query_type` | 屏蔽 SOA/PTR/HTTPS 等类型 | `on` / `off` |
| `block_ipv6` | 屏蔽 IPv6 | `on` / `off` |
| `ad_block` | 广告屏蔽 | `on` / `off` |
| `prefer_ipv4` | IPv4 优先 | `on` / `off` |
| `cn_answer_mode` | 国内域名应答模式 | `realip` / `fakeip` |
| `prefer_ipv6` | IPv6 优先 | `on` / `off` |
| `udp_fast_path` | UDP 极限快路径 | `on` / `off` |

## 配置示例

```yaml
- tag: fakeip_cache
  type: switch
  args:
    name: fakeip_cache
```

```yaml
- matches: switch 'branch_cache:on'
  exec: $cache_branch_domestic
```

```yaml
- matches: switch 'fakeip_cache:on'
  exec: $cache_fakeip_proxy
```

## 接口

- 集中读取：`GET /api/v1/control/switches`
- 单个读取：`GET /api/v1/control/switches/{name}`
- 单个修改：`PUT /api/v1/control/switches/{name}`
- 真正持久化文件：`config/custom_config/switches.yaml`
