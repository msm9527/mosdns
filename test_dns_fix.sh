#!/bin/bash
# DNS 空响应误判修复验证脚本

set -e

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo "=========================================="
echo "DNS 空响应误判修复验证"
echo "=========================================="
echo ""

# 测试域名列表
declare -A TEST_DOMAINS=(
    ["bag.itunes.apple.com"]="Apple Store"
    ["steamcdn-a.akamaihd.net"]="Steam CDN"
    ["us.download.nvidia.com"]="NVIDIA 下载"
    ["sn.api.weixin.qq.com"]="微信 API"
)

# 函数：测试单个域名
test_domain() {
    local domain=$1
    local name=$2

    echo -e "${YELLOW}测试: $name ($domain)${NC}"

    # 测试 A 记录
    echo -n "  A 记录: "
    local a_result=$(dig +short @127.0.0.1 "$domain" A 2>/dev/null)
    if [ -n "$a_result" ]; then
        echo -e "${GREEN}✓ 有响应${NC}"
        echo "    $a_result" | head -3
    else
        local a_status=$(dig @127.0.0.1 "$domain" A 2>/dev/null | grep "status:" | awk '{print $6}')
        if [ "$a_status" = "NOERROR" ]; then
            echo -e "${YELLOW}⚠ 空响应 (NOERROR)${NC}"
        else
            echo -e "${RED}✗ $a_status${NC}"
        fi
    fi

    # 测试 AAAA 记录
    echo -n "  AAAA 记录: "
    local aaaa_result=$(dig +short @127.0.0.1 "$domain" AAAA 2>/dev/null)
    if [ -n "$aaaa_result" ]; then
        echo -e "${GREEN}✓ 有响应${NC}"
        echo "    $aaaa_result" | head -3
    else
        local aaaa_status=$(dig @127.0.0.1 "$domain" AAAA 2>/dev/null | grep "status:" | awk '{print $6}')
        if [ "$aaaa_status" = "NOERROR" ]; then
            echo -e "${YELLOW}⚠ 空响应 (NOERROR)${NC}"
        else
            echo -e "${RED}✗ $aaaa_status${NC}"
        fi
    fi

    echo ""
}

# 检查 mosdns 是否运行
if ! pgrep -x "mosdns" > /dev/null; then
    echo -e "${RED}错误: mosdns 未运行${NC}"
    echo "请先启动 mosdns: systemctl start mosdns"
    exit 1
fi

echo -e "${GREEN}✓ mosdns 正在运行${NC}"
echo ""

# 测试所有域名
for domain in "${!TEST_DOMAINS[@]}"; do
    test_domain "$domain" "${TEST_DOMAINS[$domain]}"
done

# 检查 nov4/nov6 池
echo "=========================================="
echo "nov4/nov6 池状态"
echo "=========================================="

NOV4_FILE="/var/lib/mosdns/nov4list.txt"
NOV6_FILE="/var/lib/mosdns/nov6list.txt"

if [ -f "$NOV4_FILE" ]; then
    nov4_count=$(wc -l < "$NOV4_FILE" 2>/dev/null || echo 0)
    echo -e "nov4 池: ${YELLOW}$nov4_count${NC} 个域名"
    if [ $nov4_count -gt 0 ]; then
        echo "最近 5 个:"
        tail -5 "$NOV4_FILE" | sed 's/^/  /'
    fi
else
    echo -e "nov4 池: ${GREEN}不存在 (未创建)${NC}"
fi

echo ""

if [ -f "$NOV6_FILE" ]; then
    nov6_count=$(wc -l < "$NOV6_FILE" 2>/dev/null || echo 0)
    echo -e "nov6 池: ${YELLOW}$nov6_count${NC} 个域名"
    if [ $nov6_count -gt 0 ]; then
        echo "最近 5 个:"
        tail -5 "$NOV6_FILE" | sed 's/^/  /'
    fi
else
    echo -e "nov6 池: ${GREEN}不存在 (未创建)${NC}"
fi

echo ""
echo "=========================================="
echo "验证完成"
echo "=========================================="
echo ""
echo "预期结果:"
echo "1. 域名应该能正常解析 (至少有 A 或 AAAA 记录)"
echo "2. 空响应 (NOERROR 但无记录) 不应该导致后续查询失败"
echo "3. nov4/nov6 池应该只包含真正的 NXDOMAIN 域名"
echo ""
