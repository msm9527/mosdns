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
- `plugin/executable/domain_output`: 分流记忆插件，现已支持晋升阈值、衰减和 `/stats`
- `plugin/executable/requery`: 分流记忆刷新插件，现已支持旁路 refresh resolver 和 staged 刷新

## 本次同步说明

- 2026-03-06：完成分流记忆系统重构，默认启用旁路刷新链路、qtype 定向重查和规则晋升策略

- `tools`: 现已支持 `mosdns stress dns`，用于 10 万级唯一域名 UDP 压测、TCP 对照和运行态采样
