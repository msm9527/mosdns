# mosdns 项目完整深度文档

> 基于当前仓库源码生成（以代码行为为准），覆盖架构、模块、运行链路、配置、插件体系、可观测性与交付信息。

## 1. 项目定位

`mosdns` 是一个基于 Go 的 DNS 转发与规则编排引擎，支持：

- 插件化查询处理链（匹配器 + 执行器 + 服务器）
- Web UI + HTTP API 管理面
- 多种规则源（本地/在线）动态更新
- 缓存、审计、日志抓取、在线更新与自重启

代码层面入口为 [main.go](../main.go)，核心调度在 [coremain](../coremain)。

## 2. 技术栈与依赖

- 语言与运行时：Go `1.26.0`（`go.mod`）
- CLI 框架：`cobra`
- HTTP 路由：`chi`
- 配置加载：`viper` + `mapstructure`
- DNS 协议：`miekg/dns`
- QUIC：`quic-go`
- 监控：`prometheus/client_golang`
- 日志：`zap`

仓库规模（扫描统计）：

- Go 源文件：208
- 测试文件：25
- 插件类型常量定义：73

## 3. 目录结构与职责

### 3.1 核心目录

- `main.go`：程序入口，注册 `version` 子命令。
- `coremain/`：
  - CLI 启动与服务化管理（`run.go`、`service.go`）
  - 配置加载与 include 递归
  - 插件注册/实例化框架
  - API 网关与内置页面路由
  - 审计、日志抓取、覆盖配置、在线更新、系统重启
- `plugin/`：插件总线与所有插件实现（数据源、匹配器、执行器、服务器）。
- `pkg/`：底层通用库（DNS 工具、上游传输、缓存结构、网络工具等）。
- `tools/`：命令行工具命令组（`probe`、`config`、`resend`）。
- `coremain/www/`：嵌入式前端资源。

### 3.2 插件分层

- `plugin/data_provider/*`：规则/集合提供者（域名、IP、在线规则源）
- `plugin/matcher/*`：匹配条件（qname、qtype、ip、rcode、表达式等）
- `plugin/executable/*`：执行动作（forward、cache、rewrite、nft、requery 等）
- `plugin/server/*`：协议服务器（UDP/TCP/HTTP/QUIC）
- `plugin/switch/switch`：具名开关型 matcher

## 4. 启动与生命周期

## 4.1 启动命令

- 主命令：`mosdns`
- 常用子命令：
  - `start -c <config> -d <working_dir>`
  - `service install/start/stop/restart/status/uninstall`
  - `version`
  - `probe ...` / `config ...` / `resend ...`

## 4.2 服务器构建链路

`coremain.NewServer()` 主要流程：

1. 处理 `GOMAXPROCS`
2. 切换工作目录（如提供 `-d`）
3. 加载主配置（`loadConfig`）
4. 计算 `MainConfigBaseDir`（配置与附属文件根目录）
5. 初始化审计收集器（容量可持久化）
6. 构建 `Mosdns` 实例：
   - 初始化日志
   - 初始化 API 路由与指标
   - 注册内置 API
   - 启动 HTTP API 服务（若 `api.http` 配置非空）
   - 加载预置插件 + 配置插件

## 4.3 关闭行为

- 接收 `SIGINT/SIGTERM` 时触发安全关闭。
- 支持插件实现 `io.Closer` 并在关闭阶段调用。
- 审计后台 worker 关闭并回收。

## 5. 配置体系

## 5.1 主配置模型

来自 `coremain/config.go`：

```yaml
log: ...
include: []
plugins: []
api:
  http: "127.0.0.1:9099"
```

- `include` 先于当前配置加载，最大递归深度 8。
- 插件配置项为 `{tag, type, args}`。

## 5.2 运行目录关键文件

由代码实际使用：

- `config_overrides.json`：全局替换（socks5/ecs/字符串替换规则）
- `upstream_overrides.json`：上游配置覆盖
- `audit_settings.json`：审计容量持久化
- `.mosdns-update-state.json`：更新状态（可执行文件目录）
- `ui/<name>/...`：外部 UI 版本自动挂载目录

## 5.3 配置覆盖机制

加载配置插件前执行：

- `DiscoverAndCacheSettings`：发现 socks5/ecs/aliapi tags
- 若存在 `config_overrides.json`，执行 `ApplyOverrides`
- 支持按字符串规则替换并记录命中次数（用于 API 返回）

## 6. HTTP 管理面与 UI

`Mosdns.initHttpMux()` 注册：

- UI 页面：
  - `/` -> `mosdnsp.html`
  - `/graphic` -> `mosdns.html`
  - `/log` -> `log.html`
  - `/plog` -> `log_plain.html`
  - `/rlog` -> 重定向 `/log`
- 静态资源：`/assets/*`
- 指标：`GET /metrics`
- pprof：`/debug/pprof/*`
- 自动挂载外部 UI：`/{ui子目录}`（保留路径冲突会跳过）

通用行为：

- 全局 CORS 允许 `GET, POST, OPTIONS, PUT, DELETE`
- `OPTIONS` 返回 `200`
- `NotFound/MethodNotAllowed` 会输出可用路由清单

## 7. 核心功能模块

## 7.1 日志抓取

- 内存日志采集器仅采集 `DEBUG` 级日志。
- `capture/start` 会临时将日志级别切到 `DEBUG`，超时后恢复 `INFO`。

## 7.2 审计与统计

- `AuditCollector` 基于环形缓冲 + 异步 worker 批处理。
- 支持容量调整（上限 400000）与持久化。
- 提供：总请求、平均耗时、排行、慢查询、分页检索。

## 7.3 在线更新

- 默认检查 GitHub `yyysuo/mosdns` 的 `releases/latest`。
- 支持 SOCKS5 下载（从 `config_overrides.json` 读取），失败回退直连。
- 应用更新后尝试调用 `/api/v1/system/restart` 自重启。

## 7.4 系统自重启

- 非 Windows 下通过 `syscall.Exec` 进程替换。
- 重启前会定向关闭部分需要落盘的插件（如 cache/domain_output）。

## 7.5 配置导入导出

- 支持打包本地目录 ZIP 导出。
- 支持从 URL 下载 ZIP，先备份再覆盖，最后触发重启。

## 8. 插件系统深度说明

## 8.1 注册与装载

- 插件通过 `coremain.RegNewPluginFunc(type, init, argsFactory)` 注册。
- `plugin/enabled_plugins.go` 通过 blank import 激活各插件 init。
- 启动时按配置 `plugins` 顺序实例化。
- 每个插件可通过 `BP.RegAPI()` 将子路由挂载到 `/plugins/{tag}`。

## 8.2 插件类型总览（按类型常量汇总）

### 数据提供类

`domain_set, domain_set_light, sd_set, sd_set_light, ip_set, si_set, domain_mapper`

### 匹配器类

`client_ip, cname, env, has_resp, has_wanted_ans, ptr_ip, qclass, qname, qtype, random, rcode, resp_ip, string_exp, fast_mark, mark, switch, switch1..switch16`

### 执行器类

`arbitrary, black_hole, cache, debug_print, drop_resp, ecs_handler, forward, forward_edns0opt, hosts, ipset, metrics_collector, nftset, query_summary, rate_limiter, redirect, reverse_lookup, domain_output, rewrite, adguard_rule, requery, sequence, fallback, sleep, tag_setter, ttl, aliapi, cname_remover`

### 服务端类

`udp_server, tcp_server, http_server, quic_server`

## 8.3 预置插件

通过 `RegNewPersetPluginFunc` 注册的预置标签包括：

- `_sleep_500ms`
- `_remove_cname`

## 9. 插件 API 总体规范

- 基础路径：`/plugins/{plugin_tag}`（`plugin_tag` 来自 YAML 中插件 `tag`）
- 常见接口风格：
  - `GET /show`：查看当前内存规则/状态
  - `POST /post`：提交并替换规则
  - `GET /save`：将内存状态落盘
  - `GET /flush`：清空并落盘
- 详细接口见 [API_REFERENCE.md](./API_REFERENCE.md)

## 10. 可观测性与诊断

- Prometheus 指标：`/metrics`
- pprof：`/debug/pprof/*`
- 内存日志抓取：`/api/v1/capture/*`
- 审计统计与查询：`/api/v1/audit/*` + `/api/v2/audit/*`

## 11. 测试与构建状态

当前环境执行结果：

- `go test ./...` 失败
- 原因：本机 Go 版本为 `1.25.4`，而项目要求 `go 1.26.0`

错误原文：

```text
go: go.mod requires go >= 1.26.0 (running go 1.25.4; GOTOOLCHAIN=local)
```

## 12. 对接与二次开发建议

- 新增插件时优先实现清晰的 `Args` + API 路由，避免隐式配置。
- 对外接口若计划长期演进，建议补充 OpenAPI 规范与版本策略。
- 对高频接口（如 show/logs）已存在分页参数的插件，建议统一参数命名与响应结构。

