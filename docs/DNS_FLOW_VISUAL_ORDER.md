# MosDNS 解析流程（图同序版）

> 目标：尽量贴近你提供的流程图阅读顺序（自上而下主干 + 关键分叉）  
> 入口：`sequence_6666`  
> 依据：当前项目配置 `config/config.yaml` + `config/sub_config/*.yaml`

## 总览（主干同序）

```mermaid
flowchart TB
    A0([DNS 请求进入]) --> A1{qtype=SOA/PTR/HTTPS<br/>且 switch5=A ?}
    A1 -- 是 --> A2[reject 0]
    A1 -- 否 --> A3{qtype=AAAA<br/>且 switch6=A ?}
    A3 -- 是 --> A2
    A3 -- 否 --> A4[统一匹配 unified_matcher1]

    A4 --> A5{命中屏蔽类标记?<br/>1/2/3/4/5}
    A5 -- 是 --> A2
    A5 -- 否 --> A6[top_domains 统计]
    A6 --> A7[rewrite 重写]

    A7 --> A8{DDNS (fast_mark=6)?}
    A8 -- 是 --> A9[domestic 查询并 exit]
    A8 -- 否 --> A10{命中指定客户端分流?}
    A10 -- 是 --> A11[sequence_local + tag_setter + exit]
    A10 -- 否 --> A12[Prefer IPv4/IPv6 处理]

    A12 --> A13{缓存开关 switch13=A ?}
    A13 -- 是 --> A14{模式开关 switch3<br/>A=泄露 B=安全}
    A14 -- A --> A15[cache_all]
    A14 -- B --> A16[cache_all_noleak]
    A13 -- 否 --> A17[跳过主缓存]
    A15 --> A18{按 qtype 进入子流程}
    A16 --> A18
    A17 --> A18

    A18 -- A --> B0[[sequence_ipv4]]
    A18 -- AAAA --> C0[[sequence_ipv6]]
    A18 -- 其它 --> D0[[sequence_other]]
```

## A 流程（sequence_ipv4，同序）

```mermaid
flowchart TB
    B0([进入 sequence_ipv4]) --> B1[读取 unified_matcher1 预计算标记]
    B1 --> B2{灰名单 fast_mark=7?}
    B2 -- 是 --> B3[sequence_fakeip 并结束]
    B2 -- 否 --> B4{白名单 fast_mark=8?}
    B4 -- 是 --> B5[sequence_local]
    B4 -- 否 --> B9[继续消费 unified_matcher1 已设置标记]

    B5 --> B6{rcode=0/3 且无 IPv4 ?}
    B6 -- 是 --> B7[写入 my_nov4list]
    B6 -- 否 --> B8{switch9=B ?}
    B8 -- 是 --> B8A[sequence_local_fake_exit]
    B8 -- 否 --> B8B[exit]

    B9 --> B10{命中 11/12/13/14/15/16 ?}
    B10 -- 11 --> B11[sequence_local_divert]
    B10 -- 12 --> B12[sequence_fakeip]
    B10 -- 13或16 --> B13[sequence_local]
    B10 -- 14或15 --> B14[sequence_fakeip_addlist]
    B10 -- 未命中 --> B15[进入列表外判定]

    B11 --> B15
    B12 --> B15
    B13 --> B15
    B14 --> B15

    B15 --> B16{resp_ip 命中 direct_ip ?}
    B16 -- 是 --> B17[fast_mark=18 + 写 my_realiplist]
    B16 -- 否 --> B20{存在未污染 IPv4 ?}

    B17 --> B18{switch9=B ?}
    B18 -- 是 --> B19[sequence_local_fake_exit]
    B18 -- 否 --> BEND[exit]

    B20 -- 是 --> B21[fast_mark=19 + 写 my_realiplist]
    B20 -- 否 --> B22{switch3=A 泄露模式?}
    B21 --> B23{switch9=B ?}
    B23 -- 是 --> B24[sequence_local_fake_exit]
    B23 -- 否 --> BEND

    B22 -- 是 --> B25[[sequence_not_in_list_leak_v4]]
    B22 -- 否 --> B26[[sequence_not_in_list_noleak_v4]]
```

## AAAA 流程（sequence_ipv6，同序）

```mermaid
flowchart TB
    C0([进入 sequence_ipv6]) --> C1[读取 unified_matcher1 预计算标记]
    C1 --> C2{灰名单 fast_mark=7?}
    C2 -- 是 --> C3[sequence_fakeip 并结束]
    C2 -- 否 --> C4{白名单 fast_mark=8?}
    C4 -- 是 --> C5[sequence_local]
    C4 -- 否 --> C9[继续消费 unified_matcher1 已设置标记]

    C5 --> C6{rcode=0/3 且无 IPv6 ?}
    C6 -- 是 --> C7[写入 my_nov6list]
    C6 -- 否 --> C8{switch9=B ?}
    C8 -- 是 --> C8A[sequence_local_fake_exit]
    C8 -- 否 --> C8B[exit]

    C9 --> C10[规则分流同 IPv4 的 11/12/13/14/15/16]
    C10 --> C11{resp_ip 命中 direct_ip ?}
    C11 -- 是 --> C12[fast_mark=18 + 写 my_realiplist]
    C11 -- 否 --> C15{无可用 IPv6 或污染?}

    C12 --> C13{switch9=B ?}
    C13 -- 是 --> C14[sequence_local_fake_exit]
    C13 -- 否 --> CEND[exit]

    C15 -- 是 --> C16[fast_mark=20 + 写 my_nov6list]
    C15 -- 否 --> C19[fast_mark=21 + 写 my_realiplist]
    C16 --> C17[drop_resp + reject 0]
    C17 --> C18[exit]

    C19 --> C20{switch9=B ?}
    C20 -- 是 --> C21[sequence_local_fake_exit]
    C20 -- 否 --> C22{fast_mark=17(未命中)?}
    C22 -- 否 --> CEND
    C22 -- 是 --> C23{switch3=A 泄露模式?}
    C23 -- 是 --> C24[[sequence_not_in_list_leak_v6]]
    C23 -- 否 --> C25[[sequence_not_in_list_noleak_v6]]
```

## 列表外 IP 对比（sequence_not_in_list_ipmatch，同序）

```mermaid
flowchart TB
    E0([进入 sequence_not_in_list_ipmatch]) --> E1{resp_ip 命中 direct_ip ?}
    E1 -- 是 --> E2[fast_mark=24 + 写 my_realiplist]
    E2 --> E3{switch9=B ?}
    E3 -- 是 --> E4[sequence_local_fake]
    E3 -- 否 --> E5[exit]

    E1 -- 否 --> E6{resp_ip 非 geoip_cn ?}
    E6 -- 是 --> E7[fast_mark=25 -> sequence_fakeip_addlist]
    E6 -- 否 --> E8[写 my_realiplist]
    E8 --> E9{switch9=B ?}
    E9 -- 是 --> E4
    E9 -- 否 --> E5
```

## 非 A/AAAA（sequence_other）

```mermaid
flowchart TB
    D0([进入 sequence_other]) --> D1[读取 unified_matcher1 预计算标记]
    D1 --> D2{fast_mark=16 (geosite_cn) ?}
    D2 -- 是 --> D3[sequence_local]
    D2 -- 否 --> D4[sequence_google]
```
