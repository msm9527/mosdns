# 项目上下文

## 技术栈

- Go 1.26
- `go mod`
- 核心运行逻辑以插件体系组织

## 当前关注模块

- `plugin/executable/cache`: DNS 缓存插件，现已拆分为 runtime、persistence、metrics、stats、API 辅助层
- `pkg/cache`: 通用缓存容器，提供并发安全的内存缓存能力

## 本次同步说明

- 2026-03-06：完成缓存插件生产级重构，新增 snapshot + WAL、`/stats`、更细指标与测试基准
