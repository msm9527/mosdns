# MosDNS 解析流程（当前缓存架构版）

> 依据当前仓库：`config/config.yaml` + `config/sub_config/*.yaml`

## 1. 总体原则

- 入口层 `sequence_common_precheck` 只做前置检查，不再直接承担主缓存。
- `main_cache` 只下沉到真实解析链内部，因此不会再把 fakeip 应答混进主缓存。
- `branch_cache` 只覆盖真实解析分支缓存：
  - `cache_branch_domestic`
  - `cache_branch_foreign`
  - `cache_branch_foreign_ecs`
- `fakeip_cache` 只覆盖 fakeip 响应缓存：
  - `cache_fakeip_domestic`
  - `cache_fakeip_proxy`
- `probe_cache` 只覆盖 `sequence_sbnode` 的探测缓存：`cache_probe`
- `my_fakeiplist` 仍是记忆池，不属于响应缓存。

## 2. 主入口

```mermaid
flowchart TD
    A1["请求进入 sequence_6666"] --> A2["sequence_common_precheck"]
    A2 --> A3["top_domains + rewrite"]
    A3 --> A4{"qtype"}
    A4 -- A --> A5["sequence_ipv4"]
    A4 -- AAAA --> A6["sequence_ipv6"]
    A4 -- other --> A7["sequence_other"]
```

## 3. 真实解析缓存链

```mermaid
flowchart TD
    B1["sequence_local / sequence_google / sequence_google_node"] --> B2{"main_cache:on"}
    B2 -- yes --> B3["cache_main"]
    B2 -- no --> B4{"branch_cache:on"}
    B3 --> B4
    B4 -- domestic --> B5["cache_branch_domestic"]
    B4 -- foreign --> B6["cache_branch_foreign"]
    B4 -- foreign ECS --> B7["cache_branch_foreign_ecs"]
    B4 -- off --> B8["raw upstream"]
    B5 --> B8
    B6 --> B8
    B7 --> B8
    B8 --> B9["domestic / foreign / foreignecs + cname_remover"]
```

## 4. FakeIP 缓存链

```mermaid
flowchart TD
    C1["sequence_local_fake_generated"] --> C2{"fakeip_cache:on"}
    C2 -- yes --> C3["cache_fakeip_domestic"]
    C2 -- no --> C4["cnfake"]
    C3 --> C4
    C4 --> C5["ttl 5"]

    D1["sequence_fakeip_generated(_addlist)"] --> D2{"fakeip_cache:on"}
    D2 -- yes --> D3["cache_fakeip_proxy"]
    D2 -- no --> D4["nocnfake"]
    D3 --> D4
    D4 --> D5["可选写入 my_fakeiplist"]
```

## 5. 节点探测链

```mermaid
flowchart TD
    E1["sequence_sbnode"] --> E2{"probe_cache:on"}
    E2 -- yes --> E3["cache_probe"]
    E2 -- no --> E4["sleep 1000"]
    E3 --> E4
    E4 --> E5["sbnodefallback"]
    E5 --> E6["sequence_google_node_raw"]
    E5 --> E7["sequence_local_raw"]
```

## 6. 关键结论

- 关闭 `fakeip_cache` 后：
  - `cache_fakeip_domestic` 与 `cache_fakeip_proxy` 不再参与读写
  - `udp_fast_path` 也不会再复用 fakeip 结果
- 切换 `cn_answer_mode` 后，前端会主动清空核心响应缓存，降低旧 realip/fakeip 结果残留概率。
