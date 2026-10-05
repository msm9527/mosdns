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

## 6. 审计容量与磁盘写入

审计继续按配置的批次大小和时间保存查询历史，不需要关闭审计来避免容量维护反复重写整库。默认优先按约 1 MiB 估算工作预算合批，最长 300 秒保存一次，4096 条作为额外保护上限，先达到任一条件就落盘。入口队列最多约 8192 个槽位，并保留独立的 4 MiB 记录预算，增大批次不再扩大入口队列。较大单条记录会先保存已有尾批，再直接写入，不截断答案。`flush_interval_ms` 可设置为 1 至 300000 毫秒，独立核心保留已有显式值；配套 MSM 容器升级会将所管理的审计批次与间隔更新为产品推荐默认值。原始历史保留 7 天，分钟和小时统计保留 30 天。时间保留与容量淘汰共同生效，容量不足时最旧记录可能提前过期。

审计写事务将 SQLite 自动 checkpoint 阈值设为 8192 页，让相邻批次合并数据库页更新。4 KiB 页下对应约 32 MiB 的触发阈值，这不是 WAL 文件大小的硬上限，长读事务可能推迟回收。该设置在借用写连接时重新应用，连接替换后也生效。缓冲批次、同步模式、容量淘汰和正常关闭保存保持原有语义，control.db 不因审计策略改变同步阈值。

查询事件仍通过有界分片队列接收，所有分片共享一个持久化批次，避免低流量下每个分片分别提交并重复改写相同统计页。队列已满时不等待磁盘，概览继续报告降级状态。正常关闭时排空所有队列并保存尾批，显式清空历史后旧批次不会重新出现。入口队列同时受条数和共享 4 MiB 估算字节预算限制，超限事件计入丢弃统计并保持降级标志。待落盘预算还为答案 JSON 转义、临时查询镜像和结果编码预留空间，大答案因此更早落盘。缓冲字节预算不是整个进程的内存上限。查询、SQLite 和 DNS 请求自身仍需内存。

尚未落盘的记录参与历史搜索、分页、排行和统计，落盘前后的记录 ID 不变。它们仅保存在内存，进程崩溃或断电会丢失尾部。正常关闭会保存全部已接收记录，持久化失败时保留缓冲重试。关闭阶段重试次数有界，仍失败时明确报告未保存数量。切换数据库路径前先保存旧缓冲，失败时继续使用原路径。

启动时审计库尚未打开，已接收事件会留在现有有界队列中，打开成功后再写入历史及统计。打开失败不会阻塞 DNS 请求，但队列仍有容量上限。队列溢出会直接计入实时丢弃统计，存储随后打开成功不会清除这次丢失的降级标志，显式清空历史时才重置。若关闭服务时库仍不可用，无法保存的当前代次事件会计入丢弃统计，并记录警告和降级状态，不会报告为已保存。

新增记录、分钟和小时统计与本批次的有界容量淘汰在同一事务中提交，减少重复改写页面。普通容量淘汰错误会回滚本次淘汰，保留已经成功提交的新记录和统计，并报告降级。若错误使整个事务失效，则报告写入失败。完整 DNS 答案和答案搜索能力保留，存储不再重复写入无人读取的派生文本。

`max_storage_mb` 控制有效数据库页面的预算。达到预算的 90% 时开始淘汰最旧原始记录，目标为 85%。每次工作有删除行数上限，写入批次和周期维护都会推进淘汰，因此已经超额的大库会逐步收敛。启动时若已有库高于 85%，继续向低水位收敛，避免重启打断淘汰。原始记录全部过期后仍超额时，先淘汰最旧分钟统计，再淘汰小时统计。固定表结构也无法容纳时报告错误，不重复压缩数据库。

空闲页留给后续记录复用，周期维护不执行 `VACUUM`。一次周期容量清理中的有界删除共同提交，失败时回滚本次容量清理，避免部分删除后留下不匹配的淘汰水位。已有数据库文件不会立即缩小，物理占用还包含 WAL 和共享内存文件，不能把有效页面预算当成文件系统硬配额。需要释放旧文件高水位空间时，可在确认不再需要历史后执行显式清空，清空操作仍会压缩数据库。容量淘汰保留 SQLite 原有同步设置，不增加断电耐久性保证。

## 7. 域名池持久化与刷新

域名池保存仍以完整内存快照为准，在同一个 SQLite 事务内只更新有变化的域名、变体和元数据，并删除快照中已不存在的条目。未变化的行保留原来的更新时间。保存期间收到的新观察会保留待保存标记，保存失败也会重试，避免把尚未落盘的变化误认为已经保存。

按需刷新按域名池合并同一批域名的验证状态，再执行一次保存。单域名验证接口仍然可用，不支持批量验证的插件继续逐域处理。批量中某个域名不存在，不会阻止其他域名更新；调用方仍会收到该错误。以上改动不要求延长保存周期或关闭刷新，原有配置和 SQLite 事务设置继续生效。
