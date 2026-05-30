# DNS 空响应误判修复文档

## 问题背景

在使用 mosdns 进行 DNS 分流时，发现多个服务出现解析失败或访问异常：

- **Apple Store**: `bag.itunes.apple.com` 无法解析
- **Steam**: 更新下载失败
- **NVIDIA**: 显卡驱动下载失败
- **微信**: 图片加载缓慢

## 根因分析

### 问题现象

```bash
$ dig bag.itunes.apple.com A
;; QUESTION SECTION:
;bag.itunes.apple.com.          IN      A

;; ANSWER SECTION:
(空)

;; Query time: 0 msec
;; SERVER: 127.0.0.1#53(127.0.0.1)
;; WHEN: Fri May 22 16:42:16 CST 2026
;; MSG SIZE  rcvd: 38
```

响应状态为 `NOERROR`，但没有 ANSWER 记录。

### 错误逻辑

原配置使用以下条件判断域名"无记录"：

```yaml
- matches:
    - rcode 0 2 3 5
    - '!resp_ip 0.0.0.0/0'
  exec: $my_nov4list
```

**问题**：`rcode 0` (NOERROR) 有两种情况：
1. 正常响应：包含 ANSWER 记录
2. **空响应**：不包含 ANSWER 记录，但域名存在

### DNS RCODE 语义

| RCODE | 名称 | 含义 | 是否应加入 nov4/nov6 |
|-------|------|------|---------------------|
| 0 | NOERROR | 查询成功 | ❌ 否 |
| 2 | SERVFAIL | 服务器失败 | ❌ 否 |
| 3 | NXDOMAIN | 域名不存在 | ✅ 是 |
| 5 | REFUSED | 查询被拒绝 | ❌ 否 |

### 空响应 vs NXDOMAIN

**空响应 (NOERROR, no ANSWER)**：
```
域名: bag.itunes.apple.com
查询: A 记录
结果: NOERROR，但无 A 记录（域名只有 AAAA 记录）
```

**NXDOMAIN**：
```
域名: nonexistent.example.com
查询: A 记录
结果: NXDOMAIN（域名本身不存在）
```

### 误判流程

1. 客户端查询 `bag.itunes.apple.com` A 记录
2. 上游返回 NOERROR 但无 ANSWER（域名只有 AAAA）
3. mosdns 匹配 `rcode 0` 条件，将域名加入 `nov4list`
4. 后续查询被拒绝或返回空响应
5. 服务访问失败

## 修复方案

### 核心原则

**只在明确的 NXDOMAIN (rcode 3) 时才加入 nov4/nov6 池**

### 修改内容

修改前：
```yaml
- matches:
    - rcode 0 2 3 5
    - '!resp_ip 0.0.0.0/0'
  exec: $my_nov4list
```

修改后：
```yaml
# 修复：只在明确的 NXDOMAIN (rcode 3) 时才加入无V4池
# 移除 rcode 0 (NOERROR) 避免误判空响应
- matches:
    - rcode 3
    - '!resp_ip 0.0.0.0/0'
  exec: $my_nov4list
```

### 修改文件清单

| 文件 | 修改位置 | 说明 |
|------|---------|------|
| `config/sub_config/33-main-ipv4v6.yaml` | 4 处 | 主流程 IPv4/IPv6 + 节点模式 |
| `config/sub_config/32-main-not-in-list.yaml` | 4 处 | leak/noleak 模式 |
| `config/sub_config/41-refresh-not-in-list.yaml` | 4 处 | refresh leak/noleak 模式 |
| `config/sub_config/42-refresh-ipv4v6.yaml` | 3 处 | refresh 主流程 |

总计：**15 处修改**

## 验证方法

### 1. 重启服务

```bash
systemctl restart mosdns
```

### 2. 清空缓存（可选）

```bash
rm -f /var/lib/mosdns/nov4list.txt
rm -f /var/lib/mosdns/nov6list.txt
```

### 3. 运行验证脚本

```bash
./test_dns_fix.sh
```

### 4. 手动测试

```bash
# 测试 Apple Store
dig bag.itunes.apple.com A
dig bag.itunes.apple.com AAAA

# 测试 Steam
dig steamcdn-a.akamaihd.net A
dig steamcdn-a.akamaihd.net AAAA

# 测试 NVIDIA
dig us.download.nvidia.com A
dig us.download.nvidia.com AAAA

# 检查 nov4/nov6 池
cat /var/lib/mosdns/nov4list.txt
cat /var/lib/mosdns/nov6list.txt
```

## 预期效果

### 修复前

```bash
$ dig bag.itunes.apple.com A
;; status: NOERROR
;; ANSWER SECTION:
(空)

$ dig bag.itunes.apple.com AAAA
;; status: NOERROR
;; ANSWER SECTION:
(空)  # 被 nov4 误判影响
```

### 修复后

```bash
$ dig bag.itunes.apple.com A
;; status: NOERROR
;; ANSWER SECTION:
(空)  # 正常空响应

$ dig bag.itunes.apple.com AAAA
;; status: NOERROR
;; ANSWER SECTION:
bag.itunes.apple.com. 120 IN AAAA 2600:1417:...
```

## 影响评估

### 正面影响

- ✅ 修复 Apple Store、Steam、NVIDIA、微信等服务
- ✅ 降低误判率，提高解析准确性
- ✅ 减少用户投诉和故障排查成本

### 潜在影响

- ⚠️ nov4/nov6 池增长变慢（预期行为）
- ⚠️ 只记录真正的 NXDOMAIN，池大小可能减小

### 风险评估

**风险等级**: 低

**理由**：
1. 修复逻辑更严格，不会引入新的误判
2. 不影响正常的域名解析流程
3. 只改变 nov4/nov6 池的记录条件

## 监控指标

建议监控以下指标：

1. **nov4/nov6 池大小**
   - 预期：增长变慢或减小
   - 异常：快速增长（可能有新问题）

2. **DNS 查询失败率**
   - 预期：下降
   - 异常：上升（需要排查）

3. **特定域名解析成功率**
   - Apple Store: `bag.itunes.apple.com`
   - Steam: `steamcdn-a.akamaihd.net`
   - NVIDIA: `us.download.nvidia.com`
   - 微信: `sn.api.weixin.qq.com`

## 技术细节

### 为什么不用 rcode 0

**rcode 0 (NOERROR) 的两种情况**：

1. **有 ANSWER 记录**：
   ```
   ;; ANSWER SECTION:
   example.com. 300 IN A 1.2.3.4
   ```

2. **无 ANSWER 记录（空响应）**：
   ```
   ;; ANSWER SECTION:
   (空)
   ```

空响应不代表域名不存在，可能是：
- 域名只有 AAAA 记录，查询 A 记录返回空
- 域名只有 A 记录，查询 AAAA 记录返回空
- 域名有 CNAME，但最终目标无此类型记录

### 为什么不用 rcode 2 和 5

**rcode 2 (SERVFAIL)**：
- 上游服务器错误
- 应该重试，而非标记为"无记录"

**rcode 5 (REFUSED)**：
- 查询被拒绝（权限、策略等原因）
- 不代表域名不存在

### 为什么只用 rcode 3

**rcode 3 (NXDOMAIN)**：
- 明确表示域名不存在
- 权威服务器确认域名不在区域内
- 可以安全地加入 nov4/nov6 池

## 相关资源

- [RFC 1035 - Domain Names](https://www.rfc-editor.org/rfc/rfc1035)
- [DNS RCODE 定义](https://www.iana.org/assignments/dns-parameters/dns-parameters.xhtml#dns-parameters-6)
- [mosdns 文档](https://irine-sistiana.gitbook.io/mosdns-wiki/)

## 变更历史

| 日期 | 版本 | 说明 |
|------|------|------|
| 2026-05-29 | 1.0 | 初始版本，修复空响应误判问题 |

## 联系方式

如有问题或建议，请提交 Issue 或 Pull Request。
