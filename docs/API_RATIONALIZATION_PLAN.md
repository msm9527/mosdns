# API 梳理与优化方案

## 1. 文档定位

本文档用于梳理当前项目已经暴露的接口面，并提出后续优化方案。

本文档只描述现状、问题和推荐实施路线，不直接修改代码。

目标不是把所有接口“一次性重写”，而是明确：

- 哪些接口是核心稳定接口
- 哪些接口属于历史兼容接口
- 哪些接口只适合内部/插件层使用
- 哪些接口设计不合理，需要逐步收口

## 2. 当前接口面概览

基于当前代码，接口大致分成三层：

### 2.1 核心 API

主要挂在：

- `/api/v1/*`
- `/api/v2/*`

典型模块：

- 审计：`/api/v1/audit/*`、`/api/v2/audit/*`
- 覆盖配置：`/api/v1/overrides`
- 配置管理：`/api/v1/config/*`
- 更新：`/api/v1/update/*`
- 系统：`/api/v1/system/restart`
- 上游配置：`/api/v1/upstream/*`
- 运行时聚合统计：`/api/v1/cache/stats`、`/api/v1/data/domain_stats`

这部分已经是“适合给前端直接调用”的主接口层。

### 2.2 插件 API

统一挂在：

- `/plugins/{tag}/*`

问题在于这里的 `{tag}` 是**插件实例名**，不是稳定资源名。

这意味着：

- 前端直接依赖配置里的插件 tag
- 一旦插件 tag 改名，前端和文档都会受影响
- 接口路径更像“运行时内部实现暴露”，不是面向外部的稳定 API

### 2.3 历史兼容接口

当前项目里有不少明显的兼容层接口，例如：

- 同一资源同时支持 `GET/POST/PUT`
- 同一写操作同时支持 `/`、`/show`、`/post`
- 查询和操作都混在 `/plugins/{tag}` 下

这部分接口不是不能用，但不适合作为长期主接口。

## 3. 当前主要问题

## 3.1 资源边界不一致

当前部分能力已经走核心 API，例如：

- 上游配置
- 配置管理
- 更新
- 系统重启
- 审计

但仍有很多能力直接暴露为插件接口，例如：

- `switch`
- `requery`
- `adguard`
- `sd_set_light / domain_set_light / ip_set`
- `cache`

结果是：

- UI 既调用 `/api/*`，又大量调用 `/plugins/*`
- 文档和实现边界不清晰
- 前端耦合插件实例名而非业务资源名

## 3.2 HTTP 方法语义不一致

当前存在明显不合理的设计：

- `GET /plugins/{tag}/flush`
- `GET /plugins/{tag}/save`

这类接口具有副作用，但仍使用 `GET`。

另外还存在：

- 同一更新动作同时支持 `POST` 和 `PUT`
- 同一路径既支持 `/` 又支持 `/show`
- 同一写动作既支持 `/post` 又支持 `/`

这会带来几个问题：

- 不符合 HTTP 语义
- 缓存和代理层行为不可预测
- 文档解释成本高
- 前端封装难统一

## 3.3 返回格式不一致

当前返回格式主要有三种：

- JSON
- `text/plain`
- 少量二进制

尤其在插件接口中，同一类操作经常返回纯文本，例如：

- `saved`
- `ok`
- 中文提示语

这对前端统一处理不友好，常见问题包括：

- 难以统一判断成功/失败
- 无法稳定扩展字段
- toast 文案与业务结果强耦合

## 3.4 分页和查询约定不统一

当前常见查询参数虽然大致有：

- `q`
- `limit`
- `offset`

但并不统一：

- 有些接口返回 `X-Total-Count`
- 有些接口不返回
- 有些接口自己返回 `pagination`
- 有些接口只返回数组

这直接导致前端必须针对每个接口写特殊逻辑。

## 3.5 业务资源名和插件实例名耦合

例如前端当前直接调用：

- `/plugins/cache_all/flush`
- `/plugins/requery/summary`
- `/api/v1/switches`
- `/plugins/geosite_cn/config`

这些路径的问题是：

- 路径里暴露的是实现细节
- 无法保证长期稳定
- 不利于重构、重命名和多实例收敛

## 3.6 核心 API 与插件 API 职责重叠

同一能力有时已经有核心 API，但前端仍绕过去调插件接口。

典型例子：

- 统计类能力已经新增了聚合接口
- 但明细、规则、切换、刷新等仍有大量插件直调

这会让后续优化陷入被动：

- 你想统一 API，却发现 UI 已经深度绑定插件接口

## 3.7 版本边界不完整

当前有：

- `/api/v1/*`
- `/api/v2/*`

但插件接口没有版本概念。

结果是：

- 一部分能力有版本边界
- 一部分能力没有
- 实际稳定性取决于是否走插件层

## 4. 优化目标

后续 API 重构建议围绕这几个目标推进：

### 4.1 外置 UI 只优先依赖核心 API

目标：

- 外置 UI 默认只调用 `/api/v1/*` 和 `/api/v2/*`
- `/plugins/*` 逐步降级为内部接口或兼容接口

### 4.2 插件接口与业务接口分层

建议明确：

- `/api/*`：业务稳定接口
- `/plugins/*`：插件内部/兼容接口

### 4.3 统一写操作语义

目标：

- `GET` 不再承担副作用操作
- 创建用 `POST`
- 更新用 `PUT/PATCH`
- 删除用 `DELETE`
- 查询用 `GET`

### 4.4 统一返回格式

核心 API 统一返回 JSON：

```json
{
  "message": "ok",
  "status": "success"
}
```

失败统一返回：

```json
{
  "error": "xxx",
  "code": "XXX_FAILED"
}
```

### 4.5 统一分页结构

建议所有分页查询统一返回：

```json
{
  "items": [],
  "pagination": {
    "total_items": 0,
    "total_pages": 0,
    "current_page": 1,
    "items_per_page": 50
  }
}
```

### 4.6 统一资源命名

路径应该优先体现业务资源，而不是插件 tag。

例如优先考虑：

- `/api/v1/switches`
- `/api/v1/requery`
- `/api/v1/rules/adguard`
- `/api/v1/rules/diversion`
- `/api/v1/cache`
- `/api/v1/memory`

而不是直接依赖：

- `/plugins/cache_all`
- `/plugins/geosite_cn`
- `/plugins/requery`

## 5. 推荐分层模型

## 5.1 第一层：稳定业务 API

这层给 UI、脚本、文档和第三方调用。

要求：

- 路径稳定
- 返回 JSON
- 方法语义明确
- 尽量不暴露插件实现细节

建议覆盖：

- 审计
- 上游
- 覆盖配置
- 规则管理
- 分流刷新
- 开关管理
- 缓存管理
- 配置管理
- 更新管理
- 系统控制

## 5.2 第二层：内部聚合 API

当一个页面需要多个底层插件数据时，优先增加聚合接口，而不是让前端自己拼。

已经有的正面例子：

- `/api/v1/cache/stats`
- `/api/v1/data/domain_stats`
- `/plugins/requery/summary`

后续建议继续推广这类设计。

## 5.3 第三层：插件兼容接口

保留 `/plugins/*`，但定位要明确成：

- internal
- compat

主要服务于：

- 调试
- 迁移阶段兼容
- 少量特定插件运维

不建议继续让主 UI 深度依赖这层。

## 6. 具体优化建议

## 6.1 开关管理

当前现状：

- 已完成收口到 `/api/v1/switches/*`
- 旧的 `/plugins/switches/*` 和 `/plugins/{switch_name}` 已移除

后续建议：

- 其它模块按同样思路收口到 `/api/*`
- 不再新增任何 `switches` 兼容接口

## 6.2 分流刷新（requery）

当前现状：

- 逻辑已经较完整
- 但仍挂在 `/plugins/requery/*`

建议：

- 中期迁移到 `/api/v1/requery/*`
- `summary / trigger / cancel / scheduler / rules` 继续保留现有语义
- `/plugins/requery/*` 保留兼容

## 6.3 规则管理

当前现状：

- AdGuard 和在线分流规则都直接挂插件路径
- 规则资源名、插件实例名、类型名混杂

建议：

- 统一收口到：
  - `/api/v1/rules/adguard/*`
  - `/api/v1/rules/diversion/*`
- 由核心层完成插件 tag 映射

这样前端不必知道：

- `geosite_cn`
- `geosite_no_cn`
- `cuscn`
- `cusnocn`

## 6.4 缓存管理

当前现状：

- 统计已聚合
- 但查看、清空仍然直接调用 `/plugins/{cache_tag}/*`

建议：

- 保留插件层明细接口
- 但为 UI 常用操作补业务层 API：
  - `/api/v1/cache/items`
  - `/api/v1/cache/{tag}/flush`
  - `/api/v1/cache/{tag}/purge_domain`

## 6.5 数据/记忆库管理

当前现状：

- `/plugins/{tag}/show`
- `/plugins/{tag}/save`
- `/plugins/{tag}/flush`
- `/plugins/{tag}/post`

建议：

- 长期改造成资源式 API：
  - `GET /api/v1/memory/{tag}/items`
  - `PUT /api/v1/memory/{tag}/items`
  - `POST /api/v1/memory/{tag}/save`
  - `DELETE /api/v1/memory/{tag}/items`

其中：

- `GET /save`、`GET /flush` 应逐步淘汰

## 6.6 上游配置

这部分已经相对接近合理设计。

建议只做两点：

- 继续保留 `PUT /api/v1/upstream/config` 作为主入口
- 将 `POST /api/v1/upstream/config` 明确标注 deprecated

## 6.7 审计与日志

这部分目前是相对清晰的一块。

建议：

- 保持 `/api/v1/audit/*` + `/api/v2/audit/*`
- 后续不要再往 `/plugins/*` 扩散审计能力

## 7. 返回体统一建议

## 7.1 查询接口

统一建议：

```json
{
  "items": [],
  "pagination": {
    "total_items": 0,
    "total_pages": 0,
    "current_page": 1,
    "items_per_page": 50
  }
}
```

如果不是分页查询，至少也建议：

```json
{
  "items": []
}
```

## 7.2 操作接口

统一建议：

```json
{
  "status": "success",
  "message": "xxx"
}
```

不建议继续返回裸文本。

## 7.3 错误接口

统一建议：

```json
{
  "status": "error",
  "code": "INVALID_REQUEST",
  "error": "xxx"
}
```

## 8. 文档策略

建议把接口文档明确拆成三类：

### 8.1 Stable

对外推荐使用的接口。

### 8.2 Compat

仍可用，但不建议新代码继续依赖。

### 8.3 Internal

调试或插件内部接口，不保证长期稳定。

当前 `docs/API_REFERENCE.md` 已经比较完整，但还缺少这层“稳定性标识”。

## 9. 推荐实施阶段

## Phase 0：接口矩阵

先补一份完整矩阵：

- 路径
- 方法
- 所属模块
- 是否被前端调用
- 是否已文档化
- 稳定级别（stable / compat / internal）

这是后续收口的基础。

## Phase 1：文档与标识

先不改实现，先做：

- 文档分类
- deprecated 标记
- internal 标记
- 推荐替代接口说明

## Phase 2：为 UI 常用能力补核心 API

优先级最高的建议是：

- `switches`
- `requery`
- `rules`
- `cache detail actions`

也就是把当前 UI 最常用但仍然依赖 `/plugins/*` 的能力先收口。

## Phase 3：统一返回格式

先从新增和核心 API 开始：

- 所有新接口统一 JSON
- 老接口保持兼容
- 前端优先切新接口

## Phase 4：淘汰明显不合理的接口

重点处理：

- `GET /flush`
- `GET /save`
- `/post`
- 重复 `POST/PUT`

方式不是硬删，而是：

- 标 deprecated
- 增加替代路径
- 前端迁移完成后再考虑移除

## 10. 最终建议

当前项目的接口面不是“完全混乱”，而是已经有了一个较好的核心 API 雏形，但插件接口历史包袱仍然很重。

最合理的方向不是推翻重来，而是：

1. 继续把 UI 能力往 `/api/*` 收口
2. 把 `/plugins/*` 降级成兼容/内部层
3. 统一方法语义和返回结构
4. 逐步淘汰 `GET` 带副作用、`/post`、裸文本返回等不合理设计

一句话总结：

后续接口治理的核心，不是“再加更多接口”，而是**收边界、去耦合、稳语义、保兼容**。
