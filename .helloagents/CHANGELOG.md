## [0.1.1] - 2026-03-06

### 修复
- **[cache]**: 落地生产 WAL 配置，并将主仪表盘与旧版缓存卡片统一切换到 `/stats` 运行态接口 — by msm
  - 方案: [202603061104_cache-wal-ui-rollout](archive/2026-03/202603061104_cache-wal-ui-rollout/)
  - 决策: cache-wal-ui-rollout#D001(缓存 UI 统一以 `/stats` 作为主数据源)
- **[docs]**: 新增 WAL 灰度发布与回滚手册，补齐启用前检查、观测点和异常处理 — by msm

## [0.1.0] - 2026-03-06

### 修复
- **[cache]**: 重构缓存插件，新增 snapshot + WAL、`/stats` 和更细粒度指标，并补齐测试与 benchmark — by msm
  - 方案: [202603061037_cache-runtime-rebuild](archive/2026-03/202603061037_cache-runtime-rebuild/)
  - 决策: cache_runtime_rebuild#D001(采用插件内纵向重构，保持现有插件与 API 兼容)
