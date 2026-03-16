# mosdns V2 运行态运维手册

## 1. 文档定位

本文档说明 V2 重构后的运行态真源、稳定 API 与 CLI 命令。

适用对象：

- 日常运维
- 前端 / 后端联调
- V2 配置维护者

## 2. 当前真源模型

### 2.1 运行态真源

当前运行态以 SQLite 为真源，默认数据库文件位于运行目录下：

- `control.db`

主要模块包括：

- `webinfo`
- `requery`
- `adguard_rule`
- `diversion_rule`
- `domain_pool_meta / domain_pool_domain / domain_pool_variant`
- `system_event`

### 2.2 文件系统角色

文件系统只承担：

- 静态主配置和规则源
- 用户自定义控制配置
- 缓存 dump / WAL
- 显式导出的动态规则文件

动态文件不是运行态真源。

对于 `config/custom_config/*.yaml`：

- `global_overrides.yaml` 是用户可手改的 `socks5 / ecs / replacements` 真源
- `upstreams.yaml` 是用户可手改的上游 DNS 组真源
- `switches.yaml` 是用户可手改的功能开关真源
- `clientname.yaml` 是用户可手改的客户端别名真源
- 前端保存会直接改这些文件，并触发热重载
- 手工修改后重启，也会按文件内容生效
- 这两类配置不再写入 SQLite

对于动态域名池：

- `domain_memory_pool` / `domain_stats_pool` 的运行态累计、dirty 状态和发布状态都只写 SQLite `domain_pool_*` 表
- `domain_set` / `domain_set_light` 通过 `generated_from` 直接订阅对应 pool 插件，不再依赖中间导出文件
- `config/gen/` 不再承担运行态真源角色

## 3. 稳定 API

### 3.1 运行态概览

- `GET /api/v1/control/summary`
- `GET /api/v1/control/health`
- `GET /api/v1/control/events`

### 3.2 自定义控制配置

- `GET /api/v1/control/overrides`
- `POST /api/v1/control/overrides`
- `GET /api/v1/control/upstreams`
- `PUT /api/v1/control/upstreams`
- `POST /api/v1/control/upstreams`

说明：

- 这些接口读写的是 `config/custom_config/*.yaml`
- 保存成功后会尝试热重载
- 如果你是手工改文件，则重启后生效

### 3.3 requery

- `GET /api/v1/control/requery`
- `GET /api/v1/control/requery/summary`
- `GET /api/v1/control/requery/status`
- `GET /api/v1/control/requery/jobs`
- `GET /api/v1/control/requery/runs`
- `GET /api/v1/control/requery/checkpoints`
- `POST /api/v1/control/requery/enqueue`
- `POST /api/v1/control/requery/trigger`
- `POST /api/v1/control/requery/cancel`
- `POST /api/v1/control/requery/scheduler/config`
- `POST /api/v1/control/requery/rules/save`
- `POST /api/v1/control/requery/rules/flush`

### 3.4 clientname

- `GET /api/v1/control/clientname`
- `PUT /api/v1/control/clientname`

说明：

- 这个接口直接读写 `config/custom_config/clientname.yaml`
- 保存成功后立即生效

## 4. CLI 命令

### 4.1 运行态查看

```bash
mosdns control summary -c config/config.v2.yaml
mosdns control health -c config/config.v2.yaml
mosdns control events -c config/config.v2.yaml --limit 50
mosdns control requery jobs -c config/config.v2.yaml
mosdns control requery runs -c config/config.v2.yaml --limit 20
mosdns control requery checkpoints -c config/config.v2.yaml --run-id <run-id> --limit 50
```

### 4.2 运行态动作

```bash
mosdns control requery prune -c config/config.v2.yaml --keep-runs 50 --keep-checkpoints 20
mosdns control shunt explain -c config/config.v2.yaml --domain example.com --qtype A --format table
mosdns control shunt conflicts -c config/config.v2.yaml --limit 20 --format table
```

### 4.3 配置校验

```bash
mosdns config validate -c config/config.v2.yaml
```

## 5. 运维检查

若满足以下条件，可以认为 V2 运行态工作正常：

- `/api/v1/control/summary`、`/health`、`/events` 可正常返回
- `requery` 可以看到 jobs / runs / checkpoints
- `/api/v1/data/domain_stats` 与 `/api/v1/memory/{tag}/entries` 能看到动态域名池数据
- `mosdns config validate` 通过
