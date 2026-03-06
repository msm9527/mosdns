# 任务清单: cache-wal-ui-rollout

> **@status:** completed | 2026-03-06 11:17

```yaml
@feature: cache-wal-ui-rollout
@created: 2026-03-06
@status: completed
@mode: R3
```

<!-- LIVE_STATUS_BEGIN -->
状态: completed | 进度: 9/9 (100%) | 更新: 2026-03-06 11:21:00
当前: 已完成验证、归档和知识库同步
<!-- LIVE_STATUS_END -->

## 进度概览

| 完成 | 失败 | 跳过 | 总数 |
|------|------|------|------|
| 9 | 0 | 0 | 9 |

---

## 任务列表

### 1. 方案设计与包校验

- [√] 1.1 填充 `proposal.md` 和 `tasks.md`，明确配置/UI/文档范围 | depends_on: []
- [√] 1.2 校验 `202603061104_cache-wal-ui-rollout` 方案包格式 | depends_on: [1.1]

### 2. 配置落地

- [√] 2.1 在 `config/sub_config/cache.yaml` 中为所有缓存实例补齐 `wal_file` 和 `wal_sync_interval`，并增加灰度/回滚说明 | depends_on: [1.2]

### 3. 主仪表盘缓存面板

- [√] 3.1 在 `coremain/www/assets/js/log.js` 中将缓存面板数据源切换到 `/plugins/<tag>/stats` 并更新渲染逻辑 | depends_on: [1.2]
- [√] 3.2 在 `coremain/www/log.html` 与 `coremain/www/log.min.html` 中同步缓存表头 | depends_on: [3.1]

### 4. 旧版缓存卡片

- [√] 4.1 在 `coremain/www/mosdns.html` 中将缓存卡片切换到 `/stats` 并补齐运行态字段 | depends_on: [1.2]
- [√] 4.2 在 `coremain/www/mosdnsp.html` 中同步相同改造 | depends_on: [4.1]

### 5. 文档与验证

- [√] 5.1 更新 `docs/API_REFERENCE.md` 与知识库变更记录 | depends_on: [2.1, 3.2, 4.2]
- [√] 5.2 执行前端/配置相关验证并归档方案包 | depends_on: [5.1]

---

## 执行日志

| 时间 | 任务 | 状态 | 备注 |
|------|------|------|------|
| 2026-03-06 11:04:00 | 1.1 | 完成 | 方案包已创建并补齐提案、任务拆解和决策 |
| 2026-03-06 11:06:00 | 1.2 | 完成 | `validate_package.py` 通过 |
| 2026-03-06 11:08:00 | 2.1 | 完成 | 生产缓存实例已补齐 `wal_file` / `wal_sync_interval` |
| 2026-03-06 11:12:00 | 3.1 | 完成 | 主仪表盘缓存面板已改为读取 `/plugins/<tag>/stats` |
| 2026-03-06 11:13:00 | 3.2 | 完成 | `log.html` 与 `log.min.html` 缓存表头已同步 |
| 2026-03-06 11:15:00 | 4.1 | 完成 | `mosdns.html` 缓存卡片已切换到 `/stats` |
| 2026-03-06 11:15:30 | 4.2 | 完成 | `mosdnsp.html` 缓存卡片已同步改造 |
| 2026-03-06 11:17:00 | 5.1 | 完成 | API 参考和 cache 模块知识库已同步 |
| 2026-03-06 11:21:00 | 5.2 | 完成 | 已通过静态验证并归档方案包 |

---

## 执行备注

> 记录执行过程中的重要说明、决策变更、风险提示等
- 旧版 `mosdns.html` / `mosdnsp.html` 仍存在缓存卡片并直接读取 `/metrics`，本次一并切换到 `/stats`
- `log.min.html` 实际加载 `assets/js/log.min.js`，因此同步生成了新的压缩产物
- 系统级资源指标仍保留 `/metrics`；本次只收敛缓存运行态数据源
