# mosdns V2 重构状态复盘、缺口清单与后续优化建议

> 更新时间：2026-03-14  
> 分支基线：`dev`  
> 适用对象：项目维护者、后端开发者、前端开发者、运维人员

本文档用于系统性回答以下问题：

1. 这次 V2 重构**已经完成了什么**。
2. 当前还有哪些**未完成项**。
3. 现阶段哪些设计已经可用，哪些仍处于**兼容期**。
4. 目前架构中有哪些**待优化点**与**不够合理的地方**。
5. 后续建议按照什么顺序继续推进。

本文档不替代：

- [mosdns V2 完全重构蓝图](./REARCHITECTURE_V2.md)
- [mosdns V2 重构实施清单](./REARCHITECTURE_V2_IMPLEMENTATION_CHECKLIST.md)
- [mosdns V2 重构交付总结](./REARCHITECTURE_V2_DELIVERY_SUMMARY.md)
- [mosdns V2 运行态与迁移运维手册](./RUNTIME_V2_OPERATOR_GUIDE.md)

而是从“**当前真实落地状态**”角度做一次阶段性复盘。

---

## 1. 重构目标回顾

本轮 V2 重构的核心目标并不是增加更多 DNS 功能，而是把原本分散、脆弱、耦合严重的控制面与运行态管理逻辑收口，建立更清晰的系统边界：

- `config v2`：承接**静态、声明式配置**。
- `runtime.db`：承接**运行态真源**。
- 文件系统：保留**静态规则源、模板、导出物、兼容物**。
- 前端与外部调用：逐步统一到**稳定 runtime/core API**。
- 现有 plugin/sequence：第一阶段继续作为**兼容执行内核**。

换句话说，这次重构主要解决的是：

- “多个 JSON 文件同时当真源”
- “前端和插件内部实现强耦合”
- “运行态状态无法统一观测与治理”
- “动态规则导出与真实状态不一致”
- “requery 等任务系统难以审计、恢复、裁剪”

---

## 2. 当前已经完成的部分

下面按模块拆开说明。

### 2.1 运行态真源收口到 SQLite

当前已经完成或基本完成 SQLite 真源收口的运行态模块包括：

- 审计（audit）
- 全局 overrides
- upstream overrides
- switch 状态
- webinfo 动态数据
- requery 配置 / 状态 / job / run / checkpoint
- adguard 规则配置
- diversion 规则源配置
- generated datasets（动态规则产物元数据）

这意味着：

- 运行中会变的数据，已经不应再以散落 JSON/TXT 文件作为唯一真源。
- 文件路径更多承担：
  - 兼容导出
  - 历史兼容
  - 供旧链路/旧脚本读取

### 2.2 SQLite schema 收敛已经进入结构化阶段

当前不再只依赖泛化的 `runtime_kv`，而是已经增加了结构化表，主要包括：

- `switch_state`
- `generated_dataset`
- `global_override_state`
- `upstream_override_item`
- `adguard_rule_item`
- `diversion_rule_source`
- `requery_job`
- `requery_run`
- `requery_checkpoint`
- `system_event`
- `schema_migrations`

这些结构化表的意义在于：

- 支持更细粒度查询
- 支持更稳定的存储语义
- 支持后续索引、裁剪、治理、统计
- 降低“一个命名空间一个大 JSON blob”的维护成本

### 2.3 generated dataset 已经具备一致性治理能力

当前 `generated_dataset` 不再只是“有内容就导出”这么简单，已经具备：

- `version`
- `content_sha256`
- `last_exported_at_unix_ms`
- `last_export_status`
- `last_export_error`
- `last_verified_at_unix_ms`
- `last_verified_status`
- `last_verified_error`
- `last_file_sha256`

对应能力包括：

- 从数据库导出到文件
- 校验数据库与导出文件是否一致
- 记录导出状态
- 记录 verify 状态
- 追踪数据版本变化

已提供的运维命令：

```bash
mosdns runtime datasets list
mosdns runtime datasets export
mosdns runtime datasets verify
```

已提供的 API：

- `GET /api/v1/runtime/datasets`
- `POST /api/v1/runtime/datasets/export`
- `POST /api/v1/runtime/datasets/verify`

### 2.4 分流冲突检测与解释工具已经落地

当前已经有专门的分流分析命令：

```bash
mosdns runtime shunt explain --domain bing.com --qtype A --format table
mosdns runtime shunt conflicts --limit 20 --format table
```

已支持能力：

- 解释某个域名命中了哪些规则集合
- 显示对应的 mark / tag / output_tag
- 显示来源文件
- 输出最终决策链 `decision_path`
- 输出冲突规则列表
- 输出 JSON / table 两种格式

这部分对于解释“为什么某域名走代理/直连/拦截”已经足够实用。

### 2.5 runtime health / self-check 已经落地

当前已经有：

```bash
mosdns runtime health
```

以及：

- `GET /api/v1/runtime/health`

当前 health 会检查：

- sqlite 文件是否存在
- sqlite 是否能正常打开
- namespace 当前数量
- overrides 能否读取
- upstreams 能否读取
- generated datasets verify 状态
- recent requery runs 是否有失败

并统一给出：

- `ok`
- `warn`
- `error`

这部分已经能作为运维侧基础自检入口。

### 2.6 requery 已经具备任务系统和治理能力

当前 requery 不再只是配置文件 + 状态文件，而是已经具备：

- job
- run
- checkpoint
- recent history 查询
- prune 裁剪能力

已提供的运行态命令：

```bash
mosdns runtime requery jobs
mosdns runtime requery runs
mosdns runtime requery checkpoints
mosdns runtime requery prune
```

其中 `prune` 已支持：

- `--keep-runs`
- `--keep-checkpoints`
- `--max-run-age-days`
- `--max-checkpoint-age-days`

也就是说 requery 已经从“状态落库”推进到“历史可治理”。

### 2.7 稳定 runtime API 已经建立主骨架

当前已经存在较清晰的 runtime API 入口，包括：

- `/api/v1/runtime/summary`
- `/api/v1/runtime/resources`
- `/api/v1/runtime/health`
- `/api/v1/runtime/datasets`
- `/api/v1/runtime/datasets/export`
- `/api/v1/runtime/datasets/verify`
- `/api/v1/runtime/requery/*`

这意味着：

- 前端和外部调用已经有条件逐步摆脱对历史文件结构和插件内部实现的直接依赖。

### 2.8 config v2 基础能力已具备

当前已完成：

- `config v2` 基础 schema
- `config migrate`
- `config validate`
- 最小纯声明式编译路径
- runtime plugin 声明能力

所以 `config v2` 已经不是纯文档概念，而是有实际代码和迁移命令支撑。

### 2.9 文档交付层基本完善

已补齐的文档包括：

- 重构蓝图
- 实施清单
- 运维手册
- 交付总结
- PR 模板 / PR Body
- API 矩阵与索引

所以当前不是“只有代码，没有操作文档”的状态。

---

## 3. 当前已经可实际使用的运维/排障能力

### 3.1 runtime 观测类

```bash
mosdns runtime summary
mosdns runtime resources
mosdns runtime health
mosdns runtime events
```

### 3.2 generated datasets 类

```bash
mosdns runtime datasets list
mosdns runtime datasets export
mosdns runtime datasets verify
```

### 3.3 requery 类

```bash
mosdns runtime requery jobs
mosdns runtime requery runs
mosdns runtime requery checkpoints
mosdns runtime requery prune
```

### 3.4 分流排障类

```bash
mosdns runtime shunt explain --domain <domain> --qtype A --format table
mosdns runtime shunt conflicts --limit 20 --format table
```

从“可用性”角度看，这些能力已经足以支撑：

- 运行态排障
- 动态规则一致性排查
- requery 历史治理
- 分流冲突解释

---

## 4. 当前仍未完成的部分

下面是按“真正收尾”标准来看的未完成项。

### 4.1 config v2 尚未完全替代现有复杂配置体系

虽然 `config v2` 已有基础能力，但仍未完整替代当前现有复杂配置，尤其是：

- 大量 `include` 组合
- 复杂 sequence 编排
- 完整 fast_mark 语义映射
- 一些历史插件组合方式
- 更复杂的静态/动态配置联动表达

现阶段判断：

- `config v2`：**可用，但尚未完全替代旧主配置体系**
- 旧配置：**仍然是现网/现有完整功能表达的主要承载方式**

### 4.2 兼容文件路径尚未完全退役

虽然 SQLite 已经成为运行态真源主线，但兼容文件仍保留，并且很多路径还在兼容读写阶段。

典型包括：

- `config_overrides.json`
- `upstream_overrides.json`
- `switches.json`
- `config/webinfo/requeryconfig.json`
- `config/webinfo/requeryconfig.state.json`
- `config/adguard/config.json`
- `config/gen/*.txt`

当前状态是：

- SQLite 优先
- 文件兼容
- 还未进入“彻底删除旧读取逻辑”的阶段

### 4.3 前端最终收口未完成

虽然前端已经开始改用稳定 runtime API，但还没有进入“最终完全收口”状态。

未完成点包括：

- `runtime health` 的界面展示
- `datasets verify` 的结果可视化
- `requery prune` 的操作入口与结果展示
- 更完整的 runtime dashboard
- 旧 UI 逻辑与历史接口依赖的彻底清理

现阶段判断：

- 前端已经**向正确方向迁移**
- 但仍未达到“最终清理完旧接口依赖”的状态

### 4.4 health 仍是基础版，不是最终版

当前 `runtime health` 已经有较强的实用性，但它仍是“基础运维自检版”，不是最终运维中心。

尚未完成的增强点包括：

- table 输出
- 自动建议动作
- 与 `requery prune` 联动建议
- 与 `datasets export/verify` 联动建议
- 更细的 config/runtime 目录体检
- 更细的 API 自检
- 更细的上游/插件运行状态体检

### 4.5 requery 治理仍有继续提升空间

虽然 requery 现在已经具备查询和 prune，但仍然可以继续完善：

- 自动保留策略
- 启动时自动清理
- 不同失败类型分级治理
- checkpoint 生命周期细化
- 更细的历史统计与分析
- API 层统一的 prune 接口

也就是说，requery 当前已经具备“可治理能力”，但还不是“全自动治理体系”。

### 4.6 兼容层清理尚未开始收尾

这部分属于最后阶段工作，目前仍未真正启动：

- 彻底移除旧 JSON 读路径
- 收缩 `runtime_kv` 的兜底逻辑
- 清理旧 alias API
- 明确哪些文件只允许导出，不允许回读

这一阶段之所以还没动，主要是因为当前仍处于兼容观察期，优先保证稳定而不是激进清理。

### 4.7 更系统的性能与稳定性基线尚未完善

目前已经做过功能测试和若干压测，但从“正式收尾”角度仍然不够系统。

还缺：

- 更长期运行测试
- 更大规模 runtime.db 增长测试
- generated dataset 大量数据场景验证
- requery 长期累积场景验证
- 更系统 benchmark
- 更明确的性能基线文档

---

## 5. 当前代优化项（建议继续推进，但不是阻塞项）

这一部分不是“当前不能用”，而是“继续做会显著提升系统质量”。

### 5.1 health 增加“建议动作”

这是当前最值得继续补的一项。

建议 health 在出现 warn 时直接给出建议，例如：

- generated datasets mismatch → 建议执行 `runtime datasets export` 或排查导出物
- requery runs failed > 0 → 建议查看 runs / checkpoints
- run/checkpoint 数量过大 → 建议执行 `runtime requery prune`
- db 文件过大 → 建议进行治理与归档

### 5.2 health 增加 table 输出

当前 JSON 很适合程序消费，但人工阅读体验一般。建议补：

```bash
mosdns runtime health --format table
```

### 5.3 requery prune 增加 API

当前只有 CLI，建议后续补：

- `POST /api/v1/runtime/requery/prune`

这样前端和远程运维也能用。

### 5.4 conflicts 增加“谁会赢”字段

当前冲突工具已经能看出重叠，但后续可以直接在 conflict 结果里补：

- 当前优先级下谁会赢
- 最终出口预测

这样就不必对每个冲突都再单独跑 `explain`。

### 5.5 generated datasets 增加批量修复入口

例如：

- 一键导出所有 mismatch/missing 的 datasets
- 只导出 verify 失败项

### 5.6 runtime resources 继续瘦身

当前 `resources` 已经很有价值，但长期来看可以继续拆清楚：

- summary 类
- detail 类
- raw namespace dump 类

避免单个接口过大。

---

## 6. 当前不够合理或值得警惕的地方

这一部分不是“马上出 bug”，而是当前架构里仍然存在的一些不够理想的点。

### 6.1 仍然存在“双世界并存”问题

当前项目同时存在：

- 新的 SQLite 运行态世界
- 旧的 JSON / 文件兼容世界

这在迁移期是合理的，但长期看会带来两个问题：

1. 维护者容易搞不清楚谁是真源
2. 新代码容易继续错误地依赖旧文件路径

结论：

- 兼容期必须有，但不能无限期拖着
- 需要在未来某个阶段明确进入“只导出不回读”的清理窗口

### 6.2 `runtime_kv` 仍然承担兜底角色

虽然已经新增了很多结构化表，但 `runtime_kv` 仍然是兼容兜底层。

问题在于：

- 长期保留会让边界继续模糊
- 新模块如果图省事，容易继续往 `runtime_kv` 塞 blob

建议：

- 新模块禁止直接新增 `runtime_kv` 依赖
- 后续逐步缩小 `runtime_kv` 的责任范围

### 6.3 health 目前会触发 verify，属于“轻检查中带副作用”

当前 `runtime health` 会执行 generated datasets verify，并回写 verify 状态。

优点：

- 一次健康检查就能拿到最新一致性结果

缺点：

- health 不再是完全无副作用的只读操作
- 在严格语义上，它更像“检查 + 更新验证状态”

这不是严重问题，但需要在文档与命令说明中明确。

### 6.4 requery prune 是手动治理，还未策略化

当前 prune 很实用，但仍然依赖人工执行。

如果长期运行规模起来后，这会带来：

- 运维容易忘记治理
- 历史表膨胀可能被动出现

建议未来要么：

- 增加自动治理策略
- 要么至少在 health 里明确建议动作

### 6.5 runtime resources 的聚合边界仍偏大

当前 `runtime resources` 已经很强，但字段越来越多后可能产生问题：

- 响应体膨胀
- 前端难以细粒度按需拉取
- 后端聚合逻辑继续复杂化

建议：

- 后续逐步拆为 summary/detail/raw 三类接口

### 6.6 config v2 与旧配置体系尚未真正“权责切断”

目前 `config v2` 和旧体系并存，意味着：

- 有些能力在 v2 里表达不够完整
- 开发者仍然会优先去改旧 yaml
- v2 可能在一段时间内沦为“存在但不真正主用”

这是未来必须突破的点，否则 V2 重构会停留在“运行态改好了，但静态配置主线没切换”。

---

## 7. 当前阶段总体判断

如果按工程成熟度来评估，目前可以这样定义。

### 已经进入“可交付使用”的部分

- 运行态数据库化主线
- schema 收敛主线
- generated dataset 一致性治理
- 分流解释与冲突检测
- runtime health 基础版
- requery 查询与 prune 基础治理
- 稳定 runtime API 主入口

### 仍处于“兼容迁移中”的部分

- config v2 完整替代旧主配置体系
- 前端最终收口
- 兼容文件彻底退役
- runtime_kv 责任进一步缩减

### 仍处于“建议继续增强”的部分

- health 自动建议动作
- health table 输出
- requery 自动治理策略
- 更完整性能/稳定性基线
- conflict 结果直接给出赢家预测

---

## 8. 推荐的后续推进顺序

### 第一优先级：运维增强

建议优先做：

1. `runtime health` 增加建议动作
2. `runtime health --format table`
3. `requery prune` 增加 API
4. `datasets verify` 增加失败项修复入口

这是“最小代价换最大运维收益”的方向。

### 第二优先级：前端收口

建议推进：

1. 前端展示 health
2. 前端展示 datasets verify
3. 前端展示 requery prune/治理入口
4. 减少前端对旧接口和旧文件语义的依赖

### 第三优先级：config v2 完整替代能力

建议推进：

1. 把当前仍然只能靠旧配置表达的复杂 sequence 语义逐步补到 v2
2. 增加更多迁移 golden tests
3. 确保 v2 真正能作为长期主配置入口

### 第四优先级：兼容层收尾

建议在前面稳定后再做：

1. 把部分旧文件路径降级成只导出、不回读
2. 清理旧 alias API
3. 继续减少 `runtime_kv` 兜底范围

---

## 9. 当前状态一句话结论

一句话总结当前 V2 重构状态：

> 运行态数据库化、schema 收敛、分流可解释性、generated dataset 一致性治理、requery 历史治理和 runtime 健康检查都已经进入可用阶段；真正还未收尾的重点，主要在 config v2 的完全替代、前端最终收口以及兼容层的最终清理。

换个更直接的说法：

- **核心运行态重构已经做成了。**
- **系统已经能用，而且可观测性比以前强很多。**
- **真正没做完的不是基础设施，而是“最后 20% 的收尾整合工作”。**

