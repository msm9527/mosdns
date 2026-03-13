# 运行态状态基线盘点

## 1. 文档定位

本文档用于固化 mosdns 当前版本在重构前的运行态状态分布、动态写文件点、主要 API 边界和后续迁移优先级。

它服务于 V2 重构的 `Phase 0`：

- 回答“某个状态现在写到哪里”
- 回答“谁在读这个状态”
- 回答“谁应该最先迁进 SQLite”

## 2. 运行态真源现状

当前系统存在多种“实际真源”并存的情况：

1. YAML / 子配置
   - 负责静态启动配置和 plugin graph 编排
   - 典型入口：`config/config.yaml`

2. JSON 状态文件
   - 负责 UI 改动后的运行态配置
   - 典型文件：
     - `config_overrides.json`
     - `upstream_overrides.json`
     - `config/webinfo/clientname.json`
     - `config/webinfo/requeryconfig.json`
     - `config/webinfo/requeryconfig.state.json`
     - `config/rule/switches.json`

3. 文本/规则输出
   - 既像导出物，又在当前实现中承担部分状态表达
   - 典型目录：
     - `config/gen/`
     - `config/genblank/`
     - `config/rule/`

4. 审计与缓存持久化
   - 审计：`config/audit_logs/*.ndjson`
   - 缓存：`config/cache/*.dump`、`config/cache/*.wal`

## 3. 动态写文件点

### 3.1 审计

- 文件：
  - `audit_settings.json`
  - `audit_logs/audit-YYYY-MM-DD.ndjson`
- 代码入口：
  - `coremain/audit_persistence.go`

### 3.2 全局覆盖配置

- 文件：
  - `config_overrides.json`
- 代码入口：
  - `coremain/api_overrides.go`
  - `coremain/overrides.go`

### 3.3 上游配置

- 文件：
  - `upstream_overrides.json`
- 代码入口：
  - `coremain/api_upstream.go`

### 3.4 开关状态

- 文件：
  - `config/rule/switches.json`
- 代码入口：
  - `plugin/switch/switch/switch.go`

### 3.5 Web UI 动态信息

- 文件：
  - `config/webinfo/clientname.json`
  - 其他 `webinfo` 绑定文件
- 代码入口：
  - `plugin/executable/webinfo/webinfo.go`

### 3.6 requery 配置与状态

- 文件：
  - `config/webinfo/requeryconfig.json`
  - `config/webinfo/requeryconfig.state.json`
- 代码入口：
  - `plugin/executable/requery/requery_config_store.go`

### 3.7 动态规则生成

- 文件：
  - `config/gen/*.txt`
  - `config/genblank/*.txt`
- 代码入口：
  - `plugin/executable/domain_output/*`
  - `plugin/executable/requery/*`

### 3.8 在线更新状态

- 文件：
  - `.mosdns-update-state.json`
- 代码入口：
  - `coremain/update_manager.go`

## 4. 当前 API 边界

### 4.1 已经相对稳定的核心 API

- `/api/v1/audit/*`
- `/api/v2/audit/*`
- `/api/v1/overrides/*`
- `/api/v1/upstream/*`
- `/api/v1/config/*`
- `/api/v1/update/*`
- `/api/v1/system/*`

### 4.2 仍然带有实现细节泄漏的边界

- 部分能力本质仍绑定插件实例
- 部分接口实际读写的是某个 JSON 文件
- 前端仍会被内部 plugin tag 或文件结构影响

## 5. 迁移优先级

建议按收益和风险排序：

### P0

- 审计日志
- SQLite 基础设施

### P1

- switch 状态
- overrides
- upstream 配置
- webinfo / clientname

### P2

- requery 任务与恢复点
- 动态规则元数据

### P3

- config v2
- 前端稳定核心 API 全切换

## 6. 文件真源与导出物的重定义

V2 重构后建议重新定义：

### 6.1 真源

- SQLite
- config v2

### 6.2 导出物

- `config/gen/*.txt`
- `config/rule/*.txt` 中的兼容输出
- SRS 文件
- 兼容 JSON 文件

### 6.3 保留的特殊路径

- cache dump / WAL
- 导入导出 ZIP
- 静态模板和外部规则源

## 7. 结论

当前项目的核心问题不是“状态太少”，而是“状态真源太多”。  
V2 重构必须把“运行态可变状态”统一收口，否则只会继续在 JSON 文件和插件 API 上叠复杂度。
