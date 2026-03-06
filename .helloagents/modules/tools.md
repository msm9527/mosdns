# tools 模块

## 涉及路径

- `tools/init.go`
- `tools/probe.go`
- `tools/resend.go`
- `tools/stress.go`
- `tools/stress_support.go`
- `tools/stress_test.go`
- `docs/STRESS_TEST_GUIDE.md`

## 职责

### `tools/stress.go` / `tools/stress_support.go`

- 提供 `mosdns stress dns` 子命令
- 支持“真实域名冷启动 + 热点重复访问”的 UDP 压测
- 支持少量 TCP 对照压测
- 支持导入域名文件、远程 geosite 列表，并在真实域名不足时按需补位
- 输出 JSON 报告和失败样本 NDJSON
- 报告会按阶段拆分正向结果、`NXDOMAIN`、`SERVFAIL`、超时和传输错误
- 冷启动与重复访问同时存在时，额外输出 `cache_effect` 汇总，直接对比缓存阶段收益

### `docs/STRESS_TEST_GUIDE.md`

- 说明压测命令、参数、推荐用法和结果解读
- 约定 `udp-cold`、`udp-cache-hit`、`tcp-compare` 三类压测阶段的意义

## 关键实现说明

- 默认使用 `100000` 个总请求，其中目标 `10000` 个真实唯一域名作为冷启动样本
- 默认优先加载 MetaCubeX `cn/google/netflix/proxy` 四个 geosite 列表
- 重复访问阶段按热点比例分布，用于观察缓存收益
- 默认不自动补假域名，只有启用 `--allow-generated` 才会补位
- 工具只做 DNS 查询压测，不联动 `/metrics`、`/plugins/*/stats` 或 `requery` 状态接口
- 结果解读不再只看 `response_rate`，而是区分正向结果命中和负结果命中，避免把 `NXDOMAIN`/`SERVFAIL` 的快速复用误看成缓存退化
