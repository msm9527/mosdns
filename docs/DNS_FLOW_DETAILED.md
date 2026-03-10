# MosDNS 解析流程（精细节点编号版）

> 依据当前项目配置整理：`config/config.yaml` + `config/sub_config/*.yaml`  
> 入口主序列：`sequence_6666`

## 1. 主流程（sequence_6666）

```mermaid
flowchart TD
    N01["N01 收到 DNS 请求<br/>进入 sequence_6666"] --> N02{"N02 switch5=A 且<br/>qtype=SOA/PTR/HTTPS ?"}
    N02 -- 是 --> N03["N03 reject 0（结束）"]
    N02 -- 否 --> N04{"N04 switch6=A 且<br/>qtype=AAAA ?"}
    N04 -- 是 --> N03
    N04 -- 否 --> N05["N05 unified_matcher1 打标"]

    N05 --> N06{"N06 switch1=A 且命中屏蔽标记?<br/>1黑名单/2无V4/3无V6/4PCDN/5广告"}
    N06 -- 是 --> N03
    N06 -- 否 --> N07["N07 top_domains 统计"]
    N07 --> N08["N08 rewrite 处理"]

    N08 --> N09{"N09 fast_mark=6 (DDNS) ?"}
    N09 -- 是 --> N10["N10 执行 domestic 后 exit"]
    N09 -- 否 --> N11{"N11 命中指定 client<br/>直连/不直连逻辑 ?"}
    N11 -- 是 --> N12["N12 sequence_local + tag_setter + exit"]
    N11 -- 否 --> N13["N13 Prefer IPv4/IPv6 处理<br/>(switch8/switch10)"]

    N13 --> N14{"N14 switch13=A ?<br/>是否开启主缓存"}
    N14 -- 否 --> N17{"N17 qtype 分类"}
    N14 -- 是 --> N15{"N15 switch3=泄露(A)<br/>还是安全(B) ?"}
    N15 -- A --> N16A["N16A cache_all"]
    N15 -- B --> N16B["N16B cache_all_noleak"]
    N16A --> N17
    N16B --> N17

    N17 -- A --> N18["N18 进入 sequence_ipv4"]
    N17 -- AAAA --> N19["N19 进入 sequence_ipv6"]
    N17 -- 其他 --> N20["N20 进入 sequence_other"]
```

## 2. A 记录子流程（sequence_ipv4）

```mermaid
flowchart TD
    V401["V401 进入 sequence_ipv4"] --> V402["V402 读取 unified_matcher1 预计算标记"]
    V402 --> V403{"V403 fast_mark=7 灰名单?"}
    V403 -- 是 --> V404["V404 sequence_fakeip（国外 fakeip）并结束"]
    V403 -- 否 --> V405{"V405 fast_mark=8 白名单?"}
    V405 -- 是 --> V406["V406 sequence_local（国内 realip）"]
    V405 -- 否 --> V410["V410 继续消费 unified_matcher1 已设置标记"]

    V406 --> V407{"V407 rcode=0/3 且无 IPv4 ?"}
    V407 -- 是 --> V408["V408 写 my_nov4list"]
    V407 -- 否 --> V409{"V409 switch9=B ?<br/>CN->fakeip 开关"}
    V409 -- 是 --> V409A["V409A sequence_local_fake_exit 并结束"]
    V409 -- 否 --> V409B["V409B exit（白名单 realip）"]

    V410 --> V411{"V411 fast_mark=11?"}
    V411 -- 是 --> V412["V412 sequence_local_divert"]
    V411 -- 否 --> V413{"V413 fast_mark=12?"}
    V413 -- 是 --> V414["V414 sequence_fakeip"]
    V413 -- 否 --> V415{"V415 fast_mark=13/16?"}
    V415 -- 是 --> V416["V416 sequence_local"]
    V415 -- 否 --> V417{"V417 fast_mark=14/15?"}
    V417 -- 是 --> V418["V418 sequence_fakeip_addlist"]
    V417 -- 否 --> V419["V419 检查 direct_ip 与返回IP质量"]

    V419 --> V420{"V420 resp_ip 命中 direct_ip ?"}
    V420 -- 是 --> V421["V421 fast_mark=18 + 写 my_realiplist"]
    V420 -- 否 --> V424{"V424 存在未污染 IPv4 ?"}

    V421 --> V422{"V422 switch9=B ?"}
    V422 -- 是 --> V423["V423 sequence_local_fake_exit 并结束"]
    V422 -- 否 --> V430["V430 exit"]

    V424 -- 是 --> V425["V425 fast_mark=19 + 写 my_realiplist"]
    V424 -- 否 --> V426{"V426 switch3=A 泄露模式?"}
    V425 --> V427{"V427 switch9=B ?"}
    V427 -- 是 --> V428["V428 sequence_local_fake_exit 并结束"]
    V427 -- 否 --> V430

    V426 -- 是 --> V431["V431 进入 sequence_not_in_list_leak_v4"]
    V426 -- 否 --> V432["V432 进入 sequence_not_in_list_noleak_v4"]
```

## 3. AAAA 子流程（sequence_ipv6）

```mermaid
flowchart TD
    V601["V601 进入 sequence_ipv6"] --> V602["V602 读取 unified_matcher1 预计算标记"]
    V602 --> V603{"V603 fast_mark=7 灰名单?"}
    V603 -- 是 --> V604["V604 sequence_fakeip 并结束"]
    V603 -- 否 --> V605{"V605 fast_mark=8 白名单?"}
    V605 -- 是 --> V606["V606 sequence_local（国内 realip）"]
    V605 -- 否 --> V610["V610 继续消费 unified_matcher1 已设置标记"]

    V606 --> V607{"V607 rcode=0/3 且无 IPv6 ?"}
    V607 -- 是 --> V608["V608 写 my_nov6list"]
    V607 -- 否 --> V609{"V609 switch9=B ?"}
    V609 -- 是 --> V609A["V609A sequence_local_fake_exit 并结束"]
    V609 -- 否 --> V609B["V609B exit"]

    V610 --> V611["V611 规则分流同 IPv4：11/12/13/14/15/16"]
    V611 --> V612{"V612 命中 direct_ip ?"}
    V612 -- 是 --> V613["V613 fast_mark=18 + 写 my_realiplist"]
    V612 -- 否 --> V616{"V616 无可用 IPv6 或污染 ?"}

    V613 --> V614{"V614 switch9=B ?"}
    V614 -- 是 --> V615["V615 sequence_local_fake_exit 并结束"]
    V614 -- 否 --> V630["V630 exit"]

    V616 -- 是 --> V617["V617 fast_mark=20 + 写 my_nov6list"]
    V616 -- 否 --> V620["V620 fast_mark=21 + 写 my_realiplist"]

    V617 --> V618["V618 drop_resp + reject 0"]
    V618 --> V619["V619 exit"]

    V620 --> V621{"V621 switch9=B ?"}
    V621 -- 是 --> V622["V622 sequence_local_fake_exit 并结束"]
    V621 -- 否 --> V623{"V623 fast_mark=17(未命中) ?"}
    V623 -- 否 --> V630
    V623 -- 是 --> V624{"V624 switch3=A 泄露模式?"}
    V624 -- 是 --> V631["V631 进入 sequence_not_in_list_leak_v6"]
    V624 -- 否 --> V632["V632 进入 sequence_not_in_list_noleak_v6"]
```

## 4. 列表外域名 IP 对比核心（sequence_not_in_list_ipmatch）

```mermaid
flowchart TD
    O01["O01 进入 sequence_not_in_list_ipmatch"] --> O02{"O02 resp_ip 命中 direct_ip ?"}
    O02 -- 是 --> O03["O03 fast_mark=24 + 写 my_realiplist"]
    O03 --> O04{"O04 switch9=B ?"}
    O04 -- 是 --> O05["O05 sequence_local_fake（国内转 fakeip）"]
    O04 -- 否 --> O06["O06 exit（保持 realip）"]

    O02 -- 否 --> O07{"O07 resp_ip 非 geoip_cn ?"}
    O07 -- 是 --> O08["O08 fast_mark=25 -> sequence_fakeip_addlist"]
    O07 -- 否 --> O09["O09 写 my_realiplist（记忆国内）"]
    O09 --> O10{"O10 switch9=B ?"}
    O10 -- 是 --> O05
    O10 -- 否 --> O06
```

## 5. 非 A/AAAA 流程（sequence_other）

```mermaid
flowchart TD
    T01["T01 进入 sequence_other"] --> T02["T02 读取 unified_matcher1 预计算标记"]
    T02 --> T03{"T03 fast_mark=16 (geosite_cn) ?"}
    T03 -- 是 --> T04["T04 sequence_local（国内）"]
    T03 -- 否 --> T05["T05 sequence_google（国外）"]
```

## 6. 关键开关速查（来自 `switch*.txt`）

- `switch1`: 是否启用屏蔽类规则（黑名单/无解析等）
- `switch3`: 泄露模式(A) / 安全模式(B)
- `switch4`: 过期缓存总开关
- `switch5`: 阻止 SOA/PTR/HTTPS
- `switch6`: 阻止 AAAA
- `switch8`: Prefer IPv4
- `switch9`: CN 域名 realip/fakeip 切换
- `switch10`: Prefer IPv6
- `switch12`: 指定 client 不科学分支
- `switch13`: 主流程缓存开关
