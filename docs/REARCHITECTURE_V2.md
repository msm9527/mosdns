# mosdns V2 完全重构蓝图

## 1. 文档定位

本文档用于定义 mosdns 在当前 `dev` 分支基础上的下一代架构目标。

目标不是在现有实现上继续补更多插件和 JSON 文件，而是把当前项目重构为：

- `config v2` 作为唯一声明式配置入口
- `SQLite` 作为运行态真源
- 文件系统仅保留静态模板、规则源和导出物
- 前端仅依赖稳定核心 API
- 现有 `plugin/sequence` 体系在第一阶段继续作为 DNS 数据面执行内核

本文档描述的是目标架构、边界和迁移原则，不代表当前仓库已经具备这些能力。

## 2. 为什么必须重构

基于当前代码和配置，项目已经明显从“DNS 规则引擎”扩展成“DNS 数据面 + 控制面 + 管理面 + 状态持久化 + 在线更新”的复合系统，但这些能力仍然散落在不同层级：

- 主配置依赖 `include` 顺序和 tag 约定，见 `config/config.yaml`
- 运行态状态分散写入多个 JSON/TXT 文件
- 前端部分能力走核心 API，部分能力仍然依赖插件侧状态
- 插件既承担数据面职责，也承担管理面和持久化职责

这会带来几个结构性问题：

1. 配置脆弱
   - `include` 顺序不能随意调整
   - tag 名称兼具“执行节点标识”和“外部契约”两种角色
   - 配置语义依赖注释和历史约定，而不是类型系统

2. 状态分散
   - 审计日志、switch、覆盖配置、上游配置、requery 状态、webinfo 数据都在各写各的文件
   - 文件是实现细节，却逐渐变成系统真源

3. 前后端耦合
   - 前端容易耦合插件实例名、文件格式和内部路径
   - 后端稍做重构，UI 和文档都要跟着改

4. 运行与治理能力不统一
   - 有 API，但资源边界不稳定
   - 有持久化，但没有统一的运行态存储模型
   - 有任务和动态规则，但没有统一的调度/恢复/导出框架

## 3. 当前实现中的重点问题

当前最明显的重构信号包括：

- 配置模型只有 `Log / Include / Plugins / API` 四块，无法表达业务意图，只能表达插件装配
- 审计日志当前以“内存窗口 + `ndjson` 文件”持久化
- `config_overrides.json`、`upstream_overrides.json`、`switches.json`、`requeryconfig.json(.state.json)`、`clientname.json` 等都承担了运行态真源角色
- `config/gen/*.txt` 和部分类规则文件既像导出物，又承担部分业务状态

这说明项目已经不适合继续以“新增文件 + 新增插件 API + 新增子配置”的方式生长。

## 4. 重构目标

### 4.1 总体目标

- 保留现有 DNS 数据面能力和性能基础
- 把运行态数据统一收口到 SQLite
- 把配置从“插件编排”提升为“业务意图声明”
- 把前端依赖从“插件实例/文件结构”迁移到“稳定资源 API”
- 把导出文件降级为兼容物，而不是系统真源

### 4.2 非目标

- 第一阶段不直接重写全部 DNS 执行内核
- 第一阶段不把高频缓存 entry 全量改存 SQLite
- 第一阶段不一次性删除所有旧接口和旧配置
- 第一阶段不要求前端一次性重写完毕

## 5. 目标架构

推荐把系统拆成五层：

### 5.1 Data Plane

职责：

- DNS 监听、解析、转发、缓存、规则执行、响应改写

第一阶段保持现有实现：

- `plugin/server/*`
- `plugin/executable/*`
- `plugin/matcher/*`
- `pkg/*`

原则：

- 热路径继续以内存和现有结构为主
- 不把高频查询路径强行绑定到 SQLite

### 5.2 Control Plane

职责：

- 管理运行态配置
- 管理动态开关、上游、覆盖规则、任务调度
- 触发配置编译、导出、热加载

建议新增：

- `internal/runtime`
- `internal/service`

这里是未来系统的“业务内核”。

### 5.3 Config Plane

职责：

- 提供 `config v2`
- 做 `v1 -> v2` 一次性迁移
- 把业务意图编译为现有 plugin graph

建议新增：

- `internal/configv2`
- `internal/compiler`
- `cmd/mosdns config migrate`

### 5.4 Storage Plane

职责：

- 统一运行态真源
- 管理 migrations、事务、备份恢复、导出快照

建议新增：

- `internal/store/sqlite`
- `internal/store/model`

核心原则：

- SQLite 负责运行态与治理态
- 文件系统负责静态输入和兼容导出

### 5.5 Management Plane

职责：

- 对外稳定 API
- UI 与 CLI 管理入口
- 指标、诊断、任务观察、变更审计

建议新增：

- `internal/api`
- `internal/httpui`

## 6. config v2 设计

`config v2` 不再直接暴露底层插件拼装细节，而改为声明式结构。

建议顶层结构：

```yaml
version: 2

server:
  log:
    level: warn
  api:
    http: 0.0.0.0:9099

storage:
  runtime:
    engine: sqlite
    path: data/runtime.db

listeners:
  - name: default
    protocol: udp_tcp
    listen: ":53"
    entry_policy: main
    audit: true

upstreams:
  groups:
    domestic:
      strategy: primary
      endpoints: [alidns, dnspod]
    foreign:
      strategy: fallback
      endpoints: [google, cloudflare, quad9]

rule_providers:
  domain_sets:
  ip_sets:
  adguard:

features:
  cache:
  audit:
  requery:
  dynamic_rules:
  ui:

routing:
  policies:
  rules:

exports:
  generated_rules:
  compatibility_files:
```

设计原则：

- 用户编辑的是意图，不是 sequence 实现细节
- `config v2` 必须可以编译为当前 plugin graph
- 对旧配置的兼容通过迁移器和编译器实现，而不是长期双维护两套语义

## 7. SQLite 作为运行态真源

### 7.1 为什么选 SQLite

参考 PowerDNS 的 SQLite backend 经验，SQLite 对单机 DNS 管理场景有几个明显优势：

- 不引入独立服务进程
- 支持事务、索引、分页、过滤和聚合
- 适合本地管理面、任务状态、日志历史和规则元数据
- 支持 `WAL`，便于读写并发

同时，参考 AdGuard DNS 的 API-first 模式，SQLite 能帮助我们把“业务资源”和“底层文件格式”解耦。

### 7.2 SQLite 负责的范围

建议进入 SQLite 的真源数据：

- 审计日志
- switch 运行态
- 全局覆盖配置
- 上游配置与 profile
- webinfo / UI 动态配置
- requery 任务、运行历史、恢复点
- 动态规则生成元数据
- 导出快照与事件日志

### 7.3 SQLite 不直接负责的范围

- 高频 DNS 查询缓存主路径
- 原始静态规则源文件
- 用户手写模板 YAML
- 大体积第三方规则包原始内容

### 7.4 建议表

- `schema_migrations`
- `runtime_kv`
- `switch_state`
- `override_rule`
- `upstream_profile`
- `upstream_endpoint`
- `audit_log`
- `audit_rollup_hour`
- `audit_rollup_day`
- `requery_job`
- `requery_run`
- `requery_checkpoint`
- `generated_dataset`
- `export_snapshot`
- `system_event`

建议默认 pragma：

- `journal_mode = WAL`
- `synchronous = NORMAL`
- `foreign_keys = ON`
- `busy_timeout = 3000`

## 8. 文件系统的新定位

重构后建议把文件划分为三类：

### 8.1 静态输入

- `config v2` 主配置
- 用户维护的模板和外部规则源
- 静态 UI 资源

### 8.2 导出物

- `config/gen/*.txt`
- `config/rule/*.txt` 中的兼容输出
- SRS 编译产物
- 必要的兼容 JSON 文件

这些文件不再是系统真源，只是导出或兼容副本。

### 8.3 兼容缓存

- cache dump / WAL
- 导入导出 ZIP
- 临时恢复快照

## 9. API 重构原则

### 9.1 目标

前端只依赖稳定业务资源 API，不依赖插件实例名、内部 tag 或某个 JSON 文件。

推荐资源接口：

- `/api/v1/system`
- `/api/v1/runtime`
- `/api/v1/switches`
- `/api/v1/upstreams`
- `/api/v1/overrides`
- `/api/v1/audit`
- `/api/v1/requery/jobs`
- `/api/v1/requery/runs`
- `/api/v1/datasets`
- `/api/v1/exports`
- `/api/v1/config`

### 9.2 设计原则

- `GET` 只做读
- `POST` 做创建或触发动作
- `PUT/PATCH` 做更新
- `DELETE` 做删除
- 返回结构统一 JSON
- UI 不再感知插件实例名

### 9.3 对功能的影响

这不会减少功能。

它改变的是“功能挂在哪里”：

- 以前功能可能挂在某个插件和文件上
- 以后功能挂在稳定资源 API 上

这样后端内部实现即使调整，前端和外部调用也不会频繁连带修改。

## 10. 规则与导出模型

用户已经确认长期目标为：

- 数据库为真源
- 文件为导出物

因此建议新增统一的导出流程：

1. 运行态写入 SQLite
2. 导出器根据数据集版本生成文本规则/SRS/兼容文件
3. 编译器或插件消费导出物
4. 导出记录写入 `export_snapshot`

这样可以解决当前 `config/gen/*.txt`、动态规则、requery 结果之间的职责混杂问题。

## 11. requery 的新定位

`requery` 不应继续只是“一个插件 + 一个 JSON 配置文件 + 一个 state 文件”。

建议将其重构为任务系统：

- `requery_job`：任务定义
- `requery_run`：执行实例
- `requery_checkpoint`：恢复点
- `system_event`：事件记录

保留现有查询逻辑，但把持久化、恢复、调度、统计从插件文件中拆出来。

## 12. 前端迁移原则

前端逐步迁移到稳定核心 API，分阶段处理：

1. 先保持旧接口不动
2. 后端新增稳定资源 API
3. 前端逐步改调新 API
4. 后端为旧接口打弃用日志
5. 最后再移除历史接口

迁移期间用户侧功能应保持一致，变化主要发生在接口边界和内部存储层。

## 13. 目录重构建议

建议新增目录：

- `internal/configv2`
- `internal/compiler`
- `internal/runtime`
- `internal/service`
- `internal/store/sqlite`
- `internal/store/model`
- `internal/api`
- `internal/exporter`

保留并逐步瘦身：

- `coremain`
- `plugin`
- `pkg`

建议重构目标：

- `coremain` 逐步退回为启动与装配层
- 业务服务迁入 `internal/*`
- 插件层只保留数据面职责

## 14. 迁移原则

### 14.1 总原则

- 先抽象，再双写，再切读，再移除旧路径
- 每个阶段都必须能验证和回滚

### 14.2 具体原则

- 审计先迁移
- 再迁运行态状态
- 再迁任务系统
- 最后引入 `config v2` 和迁移器

原因：

- 审计收益最大，风险最低
- 运行态 JSON 文件分散，适合作为第二批收口对象
- `requery` 是复杂状态机，必须在存储层稳定后再动
- `config v2` 涉及最广，应该放到有足够支撑层之后

## 15. 参考项目的可借鉴点

### 15.1 AdGuard DNS

可借鉴：

- API-first 的管理面设计
- 以“设备/规则/统计/配置”为中心的资源边界

不直接照搬：

- SaaS 化账户模型
- 多租户管理结构

### 15.2 dae

可借鉴：

- 数据面与配置意图分层
- 对“控制面配置”和“执行路径”区别对待

不直接照搬：

- eBPF/透明代理特有架构

### 15.3 PowerDNS

可借鉴：

- 后端抽象思路
- SQLite 作为单机可运维存储
- 稳定接口与后端实现解耦

不直接照搬：

- Authoritative/Recursor 的完整产品分层

## 16. 成功标准

若本次重构成功，应该达到以下结果：

1. 新增一个运行态能力时，不再优先新增 JSON 文件
2. 前端不再依赖插件实例名
3. 配置不再主要靠 `include` 顺序和手工注释维持
4. 运行态数据可以查询、分页、恢复、审计、导出
5. 动态规则和运行态任务具备统一真源
6. 数据面性能不因引入 SQLite 而出现明显退化

## 17. 下一步

本蓝图之后的正式落地顺序建议为：

1. 编写实施清单
2. 落 SQLite 基础设施
3. 先迁审计日志
4. 再迁 switches / overrides / upstreams / webinfo
5. 重构 requery 为任务系统
6. 引入 `config v2` 与迁移工具
7. 切换前端到稳定核心 API
