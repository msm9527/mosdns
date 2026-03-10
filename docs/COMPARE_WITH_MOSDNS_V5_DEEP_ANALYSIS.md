# mosdns（当前仓库）相对原版 mosdns-v5 的深度差异分析（HEAD 对 HEAD）

## 1. 对比基线与方法

- 当前仓库：`github/mosdns`
  - 分支：`main`
  - HEAD：`3b46daf`
- 原版仓库：`github/mosdns-v5`
  - 分支：`main`
  - HEAD：`525c394`
- 对比时间：2026-03-04

### 方法说明（可复现）

1. 只比较 **Git 跟踪文件**（避免 `.git` 对象噪音）：
   - `git ls-files | sort` + `comm` + `cmp`
2. 行级统计：
   - 新增文件按 `wc -l` 计入新增行
   - 删除文件按 `wc -l` 计入删除行
   - 同名修改文件用 `git diff --no-index --numstat` 计入
3. 路由统计：
   - 通过 `rg` 提取 `Get/Post/Put/Delete/Route/Mount` 等注册语句
   - 统计“路由字符串集合”增量（不含方法维度的去重）
4. 插件类型统计：
   - 通过 `PluginType = "..."` 的定义集合比对

---

## 2. 总体结论（先看这个）

当前仓库不是小修，而是围绕“可运维化 + 可视化 + 在线更新 + 动态配置覆盖 + 大量插件扩展”进行的深度演进分支，主要变化集中在：

- `coremain` 运行时主流程重构与 API 大规模扩展
- 插件生态显著扩大（40 -> 72 插件类型）
- 前端 UI 与静态资源内置化（`coremain/www/*`）
- 在线更新与配置更新链路落地
- 发布流水线与构建矩阵重做
- Go 基础版本和依赖栈整体升级

---

## 3. 变更规模统计

### 3.1 文件级统计（跟踪文件）

- 基线跟踪文件数：`176`
- 当前跟踪文件数：`259`
- 新增文件：`84`
- 删除文件：`1`
- 修改文件：`46`
- 公共文件：`175`

### 3.2 行级统计（跟踪文件）

- 参与统计文件：`131`
- 新增行：`42523`
- 删除行：`997`

> 注意：新增行中包含大量前端静态资源。

### 3.3 代码 vs 静态资源

- `coremain/www/*` 新增：`24517` 行（约 `57.66%` 的全部新增）
- `.go` 文件新增：`16576` 行，删除：`794` 行
- `.go` 维度受影响文件：`87`

### 3.4 按顶层目录（按行变化聚合）

- `coremain`：44 文件，`+28475/-16`
- `plugin`：57 文件，`+12401/-717`
- `pkg`：9 文件，`+180/-51`
- `docs`：4 文件，`+777/-0`
- `.github`：4 文件，`+132/-9`
- `tools`：2 文件，`+105/-1`

---

## 4. 运行时与核心架构层变更（coremain）

### 4.1 启动与配置基路径机制

核心变化：引入主配置基目录 `MainConfigBaseDir`，并在启动时解析，供后续 API/覆盖逻辑使用。

- 新增全局基目录变量：`coremain/run.go:37-39`
- 启动时计算配置基目录并记录：`coremain/run.go:132-155`
- `loadConfig` 回填 `cfg.baseDir`：`coremain/run.go:188-190`
- `Config` 增加 `baseDir` 字段：`coremain/config.go:26-32`
- include 相对路径解析改为基于 `cfg.baseDir`：`coremain/mosdns.go:452-464`

对比原版：原版没有 `MainConfigBaseDir` / `cfg.baseDir` 机制，include 直接用原始字符串路径（`mosdns-v5/coremain/mosdns.go:227-235`）。

### 4.2 日志/审计/捕获链路内建

新增模块：

- `coremain/capture.go`（进程日志内存采集 + TeeCore）
- `coremain/audit.go`（审计日志采集、容量管理、排行榜、慢查询等）
- 对应 API：`api.go`、`api_audit.go`、`api_audit_v2.go`

关键证据：

- NewMosdns 中启用 TeeCore 与审计 worker：`coremain/mosdns.go:68-76`
- 审计 API v1/v2 注册：`coremain/mosdns.go:113-121`
- 审计采集结构/统计能力：`coremain/audit.go:116-174, 382-387, 638-662`

### 4.3 API 面显著扩展

新增 core API 文件：

- `coremain/api.go`
- `coremain/api_audit.go`
- `coremain/api_audit_v2.go`
- `coremain/api_overrides.go`
- `coremain/api_system.go`
- `coremain/api_update.go`
- `coremain/api_upstream.go`
- `coremain/config_manager.go`

核心新路由（前缀级）：

- `/api/v1/capture/*`
- `/api/v1/audit/*`
- `/api/v2/audit/*`
- `/api/v1/overrides/*`
- `/api/v1/system/restart`
- `/api/v1/update/*`
- `/api/v1/upstream/*`
- `/api/v1/config/*`

路由集合统计：

- 基线：`11`
- 当前：`55`
- 新增：`44`

### 4.4 UI 与静态资源内置化

原版仅暴露 `/metrics` 与 `/debug/pprof` 等接口。
当前版本新增：

- embed 静态资源：`coremain/mosdns.go:45-46`
- 新页面路由：`/`, `/graphic`, `/log`, `/plog`, `/rlog`, `/assets/*`（`coremain/mosdns.go:359-364`）
- 自动挂载 `MainConfigBaseDir/ui/*` 子目录为路由（`coremain/mosdns.go:366-403`）

资源体量：`coremain/www/*` 新增约 `24517` 行（CSS/JS/HTML/字体）。

### 4.5 动态覆盖与配置发现

- 覆盖配置结构：`coremain/overrides.go:15-37`
- 启动扫描配置并发现 `socks5/ecs/aliapi tag`：`coremain/overrides.go:62-133`
- 按插件递归应用替换规则：`coremain/overrides.go:175-222`
- API 暴露覆盖读取/写入：`coremain/api_overrides.go:16-23, 40-106, 108-136`

### 4.6 在线更新与自重启机制

- 构建版本注入：
  - `main.go:34-39`（`version = "v5-ph-srs"` + `SetBuildVersion`）
  - `coremain/versioninfo.go:10-27`
- 更新管理器：`coremain/update_manager.go`（1125 行）
- 更新 API：`coremain/api_update.go:16-21`
- 系统重启 API：`coremain/api_system.go:19-22`

注意点：更新源硬编码为 `yyysuo/mosdns` 与固定 tag 逻辑（`coremain/update_manager.go:31-39`），已经明显偏离原 upstream 发布源。

---

## 5. 插件生态演进（plugin）

### 5.1 插件类型规模

- 基线插件类型：`40`
- 当前插件类型：`71`
- 新增类型：`31`

新增类型清单：

`adguard_rule, aliapi, cname_remover, domain_mapper, domain_output, domain_set_light, fast_mark, requery, rewrite, sd_set, sd_set_light, si_set, switch, switch1~switch16, tag_setter, webinfo`

### 5.2 `enabled_plugins.go` 的显式扩容

`plugin/enabled_plugins.go` 比原版新增大量 blank import（数据提供器、匹配器、执行器）：

- 新 data_provider：`domain_set_light`, `sd_set`, `sd_set_light`, `si_set`, `domain_mapper`
- 新 matcher：`fast_mark`
- 新 executable：`domain_output`, `switcher1..16`, `aliapi`, `cname_remover`, `adguard`, `webinfo`, `requery`, `rewrite`, `tag_setter`

证据：`plugin/enabled_plugins.go:25-95`。

### 5.3 插件 API 由“少量 GET/POST”扩展到 CRUD

按注册语句统计：

- 基线：`Get=8, Post=1, Route=1, Mount=1`
- 当前：`Get=68, Post=43, Put=7, Delete=5, Route=7, Mount=1`

代表性新增插件管理路由（集合）：

- `/rules`, `/rules/{id}`
- `/config`, `/config/{name}`
- `/update`, `/update/{name}`
- `/trigger`, `/cancel`, `/scheduler/config`
- `/status`, `/stats`, `/rank/*`
- `/show`, `/save`, `/post`, `/restartall`

### 5.4 Sequence 引擎重构

关键变化：

- `exec` 从单字符串扩为支持列表（多执行体）：`plugin/executable/sequence/config.go`
- 引入 `exit` 与 `try` 动作：`plugin/executable/sequence/built_in.go`
- `ChainWalker` 增加 matcher/plugin 流经日志、命名 matcher、domain_set 追踪逻辑：`plugin/executable/sequence/chain.go`
- `Sequence` 构链/执行逻辑重做：`plugin/executable/sequence/sequence.go`

这是行为级重构，不只是语法微调。

### 5.5 Cache 插件重写级别改造

`plugin/executable/cache/cache.go` 发生大规模重写（`+745/-83`，另删除 `utils.go`）：

- 分片 L1 热缓存结构（`shardCount`, `l1Shard`）
- `exclude_ip` 扩展（支持字符串/列表）
- Pool/并发限制与 lazy update 优化
- 新增插件 API：`/save`, `/show`, `/load_dump` 等

证据：`plugin/executable/cache/cache.go`（大量“性能补丁”标记）与 `deleted: plugin/executable/cache/utils.go`。

### 5.6 结构性观察：`switch` 插件文件存在，但默认未启用

- 新增文件：`plugin/switch/switch/switch.go`（`PluginType = "switch"`）
- 但 `plugin/enabled_plugins.go` 未 blank import 该包

这意味着默认构建路径下它不会自动注册为可用插件（除非有其他包显式引用）。

---

## 6. 依赖栈与构建发布链变化

### 6.1 Go 基线与依赖升级

`go.mod` 关键变化：

- Go 版本：`1.22.0 + toolchain 1.23.0` -> `1.26.0`
- mapstructure：`mitchellh/mapstructure` -> `go-viper/mapstructure/v2`
- 大量依赖升级：`chi`, `dns`, `quic-go`, `prometheus`, `x/*` 等
- 新增关键依赖：`google/uuid`, `robfig/cron/v3`, `sagernet/sing`

证据：`go.mod` diff。

### 6.2 发布流程变更

- `release.yml` 升级 actions 版本，支持 `workflow_dispatch`，`prerelease=false`
- 新增 `release-main.yml`：main 分支自动构建并发布，使用 `v5-ph-srs-YYYYMMDD-<sha>` tag 规范
- 新增 `dependabot.yml`
- `release.py` 重写构建矩阵（扩展多平台/架构）与版本生成逻辑

---

## 7. 行为层面的“用户可见变化”

相对原版，当前版本在用户体验上新增了整套“面板化运维能力”：

- 可视化页面：`/`, `/graphic`, `/log`, `/plog`
- 日志采集与审计排行（域名、客户端、domain_set、慢查询）
- 覆盖配置管理（Socks5/ECS/字符串替换）
- 上游配置在线管理（含 AliAPI 相关字段）
- 在线检查更新、应用更新、自重启
- 配置目录导出/远程 ZIP 覆盖更新

---

## 8. 风险与兼容性评估

### 8.1 运行环境风险

- Go 要求提升到 `1.26.0`，旧构建环境直接失配。
- 依赖栈升级跨度大，和原版生态兼容性不再可假设。

### 8.2 运维与安全边界

- CORS 放开到 `*` 且允许 `PUT/DELETE`（`coremain/mosdns.go:237-243`），需要配合部署层做来源与鉴权控制。
- 在线更新/配置更新涉及下载、覆盖、重启，若部署策略不完善，存在误更新风险。

### 8.3 代码质量与维护性

- 新增代码中有较多调试日志与“临时标记式注释”（如 `<<< ADDED >>>`、`[Debug ...]`），长期可维护性一般。
- `switch` 包存在但默认未启用，易造成“代码已在仓库但功能不可用”的认知偏差。

### 8.4 与原版定位的偏离

- 更新源、版本语义、README 文档定位均从上游官方偏向定制发行链路。
- 该仓库应视为“定制发行分支”，而非“原版轻量补丁”。

---

## 9. 附录

### 9.1 新增 coremain Go 文件

- `coremain/api.go`
- `coremain/api_audit.go`
- `coremain/api_audit_v2.go`
- `coremain/api_overrides.go`
- `coremain/api_system.go`
- `coremain/api_update.go`
- `coremain/api_upstream.go`
- `coremain/audit.go`
- `coremain/capture.go`
- `coremain/config_manager.go`
- `coremain/overrides.go`
- `coremain/sys_other.go`
- `coremain/sys_unix.go`
- `coremain/update_manager.go`
- `coremain/util_memory.go`
- `coremain/versioninfo.go`

### 9.2 新增 plugin Go 文件（按模块）

- data_provider：
  - `plugin/data_provider/domain_mapper/domain_mapper.go`
  - `plugin/data_provider/domain_set_light/domain_set_light.go`
  - `plugin/data_provider/sd_set/sd_set.go`
  - `plugin/data_provider/sd_set_light/sd_set_light.go`
  - `plugin/data_provider/si_set/si_set.go`
- executable：
  - `adguard`, `aliapi`, `cname_remover`, `domain_output`, `requery`, `rewrite`, `switcher1..16`, `tag_setter`, `webinfo`
- matcher：
  - `plugin/matcher/fast_mark/fast_mark.go`
- switch：
  - `plugin/switch/switch/switch.go`

### 9.3 删除文件

- `plugin/executable/cache/utils.go`

### 9.4 Top 代码文件变化（按行差）

1. `coremain/update_manager.go` `+1125/-0`
2. `plugin/executable/aliapi/aliapi.go` `+922/-0`
3. `plugin/executable/requery/requery.go` `+800/-0`
4. `plugin/executable/adguard/adguard.go` `+885/-0`
5. `plugin/executable/cache/cache.go` `+745/-83`
6. `coremain/audit.go` `+811/-0`
7. `plugin/data_provider/si_set/si_set.go` `+802/-0`
8. `plugin/executable/requery/requery.go` `+800/-0`
9. `plugin/data_provider/sd_set/sd_set.go` `+699/-0`
10. `plugin/data_provider/sd_set_light/sd_set_light.go` `+686/-0`

