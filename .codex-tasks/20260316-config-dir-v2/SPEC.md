# 任务说明

目标：把 `config/` 目录改造成可直接启动的完整 V2 配置目录，保持现有主要功能不缺失，去掉旧 `include/plugins` 主配置入口，尽量精简动态文件，并把运行态数据库统一放到 `config/db/` 下。

约束：
- 不考虑兼容旧配置入口。
- 允许破坏式重构。
- 每阶段必须验证并提交一次。
- 最终必须完成真实域名 DNS/API/UI E2E；若失败，继续修复直到通过。

验收：
- `config/config.yaml` 为纯 V2。
- `mosdns start --dir config` 可直接启动。
- `control.db` 与 `audit.db` 落到 `config/db/`。
- 核心控制面、UI、DNS 查询、真实域名 E2E 通过。
- 不再依赖 `config_overrides.json` / `upstream_overrides.json` / `webinfo/*.json` / `rule/switches.json` 作为运行态真源。
