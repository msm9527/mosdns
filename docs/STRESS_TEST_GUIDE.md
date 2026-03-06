# mosdns DNS 压测说明

## 目标

`mosdns stress dns` 用来模拟更接近日常使用的 DNS 流量，而不是只做全唯一冷查询。

默认流量模型：

- 总请求数：`100000`
- 目标唯一真实域名：`10000`
- 第一阶段：真实域名冷启动
- 第二阶段：热点重复访问，用来观察缓存收益
- 可选第三阶段：少量 TCP 对照

它只做 DNS 查询测试，不联动 HTTP API 或插件状态接口。

## 命令入口

```bash
mosdns stress dns --server '127.0.0.1:53'
```

## 默认行为

默认参数下，工具会执行三件事：

1. 尝试加载 `10000` 个真实唯一域名
2. 用 UDP 先把这些域名各查询一次
3. 把剩余请求量按热点分布重复访问，观察缓存效果

如果真实域名不足，默认不会再补假域名，而是直接使用实际加载到的真实域名数量，并在报告里写明。

## 域名来源

### 1. 直接导入域名文件

```bash
mosdns stress dns \
  --server '127.0.0.1:53' \
  --domains-file '/path/to/domains.txt'
```

支持从每行中提取最后一个像域名的字段，例如：

```text
1 example.com
2 cloudflare.com
# comment
```

工具会自动：

- 去重
- 规范成 FQDN
- 和远程源合并

### 2. 默认远程 geosite 列表

如果不提供 `--domains-file`，工具会默认读取这四个 MetaCubeX geosite 列表：

- `cn.list`
- `google.list`
- `netflix.list`
- `proxy.list`

来源：

- `https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/refs/heads/meta/geo-lite/geosite/cn.list`
- `https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/refs/heads/meta/geo-lite/geosite/google.list`
- `https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/refs/heads/meta/geo-lite/geosite/netflix.list`
- `https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/refs/heads/meta/geo-lite/geosite/proxy.list`

工具会解析 `+.domain.com`、`full:domain.com`、`domain:domain.com` 这类规则，并提取成可直接查询的真实域名。

### 3. 可选补位域名

如果你就是要凑够唯一域名量，可以显式开启：

```bash
--allow-generated
```

这时才会自动补位类似：

```text
ha-000001.example.com.
ha-000002.example.net.
```

默认关闭，因为这类域名更适合极限冷路径，不适合模拟日常缓存场景。

## 推荐用法

### 1. 日常使用口径压测

```bash
mosdns stress dns \
  --server '127.0.0.1:53' \
  --count 100000 \
  --unique-count 10000 \
  --concurrency 120 \
  --qps 300 \
  --hotset-ratio 0.2 \
  --tcp-sample 0 \
  --report 'stress-report.json' \
  --failures 'stress-failures.ndjson'
```

适合看：

- 真实域名冷启动表现
- 缓存阶段延迟下降情况
- 正常 QPS 下的稳定性

### 2. 更偏缓存收益的测试

```bash
mosdns stress dns \
  --server '127.0.0.1:53' \
  --count 100000 \
  --unique-count 5000 \
  --qps 300 \
  --hotset-ratio 0.1
```

适合看：

- 更高重复率下的缓存收益
- 热点域名集中访问时的响应质量

### 3. AAAA 压测

```bash
mosdns stress dns \
  --server '127.0.0.1:53' \
  --qtype AAAA
```

## 关键参数

| 参数 | 含义 | 默认值 |
|---|---|---|
| `--server` | DNS 服务地址 | `127.0.0.1:53` |
| `--domains-file` | 本地域名文件 | 空 |
| `--source-url` | 远程 geosite/域名列表 URL，可重复指定 | 默认四个 MetaCubeX geosite 列表 |
| `--count` | 总请求数 | `100000` |
| `--unique-count` | 冷启动阶段目标唯一真实域名数 | `10000` |
| `--concurrency` | 并发 worker 数 | `200` |
| `--qps` | UDP 发送速率限制，`0` 表示不限速 | `0` |
| `--hotset-ratio` | 重复访问阶段的热点域名占比 | `0.2` |
| `--allow-generated` | 真实域名不足时是否允许补位假域名 | `false` |
| `--tcp-qps` | TCP 对照阶段 QPS | `200` |
| `--tcp-sample` | TCP 对照样本数 | `0` |
| `--timeout` | 单次查询超时 | `2s` |
| `--report` | JSON 报告输出路径 | `stress-report.json` |
| `--failures` | 失败样本 NDJSON 输出路径 | `stress-failures.ndjson` |

## 输出文件

### 1. JSON 报告

默认输出：`stress-report.json`

报告包含：

- 请求总量
- 目标唯一域名数
- 实际真实唯一域名数
- 重复访问请求数
- 热点域名池大小
- 各阶段统计
  - `udp-cold`
  - `udp-cache-hit`
  - `tcp-compare`
- 每个阶段的结果拆分
  - `positive_responses`
  - `nxdomain_responses`
  - `servfail_responses`
  - `timeout_errors`
  - `transport_errors`
- 当存在 `udp-cold` 和 `udp-cache-hit` 时，附带 `cache_effect`
  - 用于直接对比冷启动和重复访问的正向结果率、超时率和延迟变化

### 2. 失败样本

默认输出：`stress-failures.ndjson`

每行一条失败或异常样本，包含：

- 阶段
- 协议
- 域名
- QType
- 错误信息或 RCode
- 延迟

## 如何解读结果

### `udp-cold`

看冷启动能力：

- `response_rate`
- `timeouts`
- `errors`
- `latency.p95_ms`
- `latency.p99_ms`

### `udp-cache-hit`

看缓存收益：

- 延迟是否明显低于 `udp-cold`
- `response_qps` 是否提升
- `timeouts` 是否明显下降
- `breakdown.positive_responses` 是否明显高于 `udp-cold`
- `breakdown.nxdomain_responses` / `breakdown.servfail_responses` 是否只是负结果被快速复用

### `cache_effect`

这是报告里专门给缓存阶段看的汇总项，用来避免把“负结果命中”误看成“缓存退化”。

重点字段：

- `cold_positive_rate`
- `repeat_positive_rate`
- `positive_rate_delta`
- `cold_timeout_rate`
- `repeat_timeout_rate`
- `timeout_rate_delta`
- `cold_p50_ms`
- `repeat_p50_ms`
- `cold_p95_ms`
- `repeat_p95_ms`

解读方式：

- `repeat_positive_rate` 上升，说明重复访问阶段正向结果占比更高
- `repeat_timeout_rate` 下降，说明缓存或热点复用降低了超时
- 如果 `repeat` 阶段仍然有较多 `NXDOMAIN` / `SERVFAIL`，先看是不是热点域名本来就是负结果，而不是直接归因为缓存失效

### `tcp-compare`

看 TCP 是否可用，以及 TCP 相比 UDP 是否明显更慢。

## 使用建议

- 想模拟日常使用：
  - 保持 `--unique-count 10000`
  - 使用 `--qps 200~500`
  - `--tcp-sample 0`
- 想看缓存效果：
  - 降低 `--unique-count`
  - 保持较高 `--count`
  - 观察 `udp-cache-hit`
- 想测极限冷查询：
  - 开启 `--allow-generated`
  - 或提高 `--unique-count`

## 局限说明

- 这四个 geosite 列表本身未必能提供足够的真实唯一域名
- 若真实域名不足，默认会直接降低实际唯一域名数，而不是伪造请求
- 这套工具只反映 DNS 查询侧表现，不包含 HTTP/API 健康检查
