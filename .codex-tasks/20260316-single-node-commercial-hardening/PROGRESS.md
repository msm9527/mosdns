# 进度记录

- 已完成单机商用级稳定性与易用性收口。
- 已补强 forward/aliapi 上游健康打分、级联对冲查询与取消传播。
- 已把 control summary/health/upstreams 暴露为可观测控制面，并补充 WAL/quick_check/建议动作。
- 已优化 audit 日志统计与查询路径，优先使用 SQLite 聚合与索引。
- 已完成 UI 上游健康展示、requery 触发校验、真实域名 DNS/API/UI E2E 回归。
