# mosdns V2 运行态与迁移运维手册

## 1. 文档定位

本文档用于说明 V2 重构后的运行态真源、稳定 API、CLI 命令和兼容迁移方式。

适用对象：

- 日常运维
- 前端/后端联调
- 从旧 JSON 状态迁入 SQLite
- 使用 `config v2` 进行配置迁移和校验

## 2. 当前真源模型

### 2.1 运行态真源

当前运行态以 SQLite 为真源，默认数据库文件位于运行目录下：

- `runtime.db`

主要命名空间包括：

- `overrides`
- `upstreams`
- `switch`
- `webinfo`
- `requery`
- `adguard_rule`
- `diversion_rule`
- `generated_dataset`

### 2.2 文件系统角色

文件系统现在主要承担以下角色：

- 静态主配置和规则源
- 兼容导出物
- 缓存 dump / WAL
- 配置导入导出 zip

也就是说：

- JSON/TXT 文件不再是运行态的首选真源
- 动态规则文件属于导出物或兼容物
- 旧 JSON 文件仍可导入/导出，用于兼容和回滚

## 3. 稳定 API

### 3.1 运行态概览

- `GET /api/v1/runtime/summary`
- `GET /api/v1/runtime/resources`

用途：

- 查看当前 SQLite 运行态命名空间
- 聚合读取 switch / webinfo / requery / adguard / diversion / generated dataset
- 为前端提供统一资源入口

### 3.2 requery 任务历史

- `GET /api/v1/runtime/requery/jobs`
- `GET /api/v1/runtime/requery/runs`
- `GET /api/v1/runtime/requery/checkpoints`
- `POST /api/v1/runtime/requery/enqueue`

用途：

- 查看 requery 任务定义
- 查看最近运行历史
- 查看 checkpoint 与恢复点
- 由动态规则变更触发 requery 入队

### 3.3 兼容资源

当前仍保留以下稳定资源域：

- `/api/v1/overrides`
- `/api/v1/upstream`
- `/api/v1/audit`
- `/api/v2/audit`

建议：

- 新前端优先使用 `/api/v1/runtime/*`
- 面向具体业务动作时再组合调用对应资源接口

## 4. CLI 命令

### 4.1 运行态查看

```bash
mosdns runtime summary -c config/config.yaml
mosdns runtime events -c config/config.yaml --limit 50
mosdns runtime datasets list -c config/config.yaml
mosdns runtime requery jobs -c config/config.yaml
mosdns runtime requery runs -c config/config.yaml --limit 20
mosdns runtime requery checkpoints -c config/config.yaml --run-id <run-id> --limit 50
```

### 4.2 导出兼容文件

```bash
mosdns runtime datasets export -c config/config.yaml
mosdns runtime legacy export -c config/config.yaml
```

说明：

- `datasets export`：把 SQLite 中的动态规则导回兼容文件
- `legacy export`：把 SQLite 中的运行态导回历史 JSON 文件

### 4.3 导入旧运行态

```bash
mosdns runtime legacy import -c config/config.yaml
```

已支持导入/导出的典型对象：

- overrides
- upstreams
- switch
- webinfo
- requery
- adguard

## 5. config v2

### 5.1 迁移

```bash
mosdns config migrate -c config/config.yaml -o config/config.v2.yaml
```

输出纯声明式最小子集：

```bash
mosdns config migrate -c config/config.yaml -o config/config.v2.yaml --pure-declarative
```

### 5.2 校验

```bash
mosdns config validate -c config/config.v2.yaml
```

### 5.3 当前覆盖能力

`config v2` 当前已支持的重点块包括：

- listeners
- upstreams
- policies
- rule providers
- runtime plugins（webinfo / requery / switch）

仍保留 `legacy` 兼容块，用于承接暂未声明式化的旧 plugin graph 细节。

## 6. 兼容与回滚

### 6.1 兼容策略

- 运行态优先读 SQLite
- 旧 JSON 文件仍可导入和导出
- 动态规则优先读数据库，再导出到兼容文件
- 历史接口保留兼容期

### 6.2 回滚策略

若需要暂时回退：

1. 先执行 `mosdns runtime legacy export`
2. 保留导出的 JSON/TXT 文件
3. 使用旧启动方式或旧接口继续工作

## 7. 验收视角

若满足以下条件，可以认为 V2 运行态改造已生效：

- 前端主页面能从 `/api/v1/runtime/summary` 和 `/api/v1/runtime/resources` 获取核心状态
- `requery` 可以看到 jobs / runs / checkpoints
- 删除动态规则导出文件后可从 SQLite 重新导出
- 旧 JSON 状态可以导入到 SQLite，也可以再导回文件
- `config v2` 可迁出并通过校验
