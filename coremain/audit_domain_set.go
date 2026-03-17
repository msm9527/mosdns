package coremain

import (
	"sort"
	"strings"
)

const unmatchedAuditDomainSet = "未命中"

var auditDomainSetPriority = []string{
	"BANSOA",
	"BANPTR",
	"BANHTTPS",
	"BANAAAA",
	"黑名单",
	"广告屏蔽",
	"DDNS域名",
	"灰名单",
	"白名单",
	"记忆直连",
	"记忆代理",
	"订阅直连补充",
	"订阅代理",
	"订阅代理补充",
	"订阅直连",
	unmatchedAuditDomainSet,
}

func normalizeAuditDomainSet(raw, qtype string) string {
	tags := splitAuditDomainSetTags(raw)
	if len(tags) == 0 {
		return ""
	}
	tagSet := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tagSet[tag] = struct{}{}
	}
	if picked := pickAuditDomainSetByPriority(tagSet, normalizeAuditQueryType(qtype)); picked != "" {
		return picked
	}
	return tags[0]
}

func splitAuditDomainSetTags(raw string) []string {
	parts := strings.Split(raw, "|")
	tags := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		tag := canonicalAuditDomainSetTag(part)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

func canonicalAuditDomainSetTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if tag == "unmatched_rule" {
		return unmatchedAuditDomainSet
	}
	return tag
}

func normalizeAuditQueryType(qtype string) string {
	return strings.ToUpper(strings.TrimSpace(qtype))
}

func pickAuditDomainSetByPriority(tagSet map[string]struct{}, qtype string) string {
	switch qtype {
	case "A":
		if hasAuditDomainSet(tagSet, "记忆无V4") {
			return "记忆无V4"
		}
	case "AAAA":
		if hasAuditDomainSet(tagSet, "记忆无V6") {
			return "记忆无V6"
		}
	}
	for _, tag := range auditDomainSetPriority {
		if hasAuditDomainSet(tagSet, tag) {
			return tag
		}
	}
	return ""
}

func hasAuditDomainSet(tagSet map[string]struct{}, tag string) bool {
	_, ok := tagSet[tag]
	return ok
}

func buildSortedRankItems(counts map[string]int, limit int) []AuditRankItem {
	if limit <= 0 || len(counts) == 0 {
		return []AuditRankItem{}
	}
	items := make([]AuditRankItem, 0, len(counts))
	for key, count := range counts {
		items = append(items, AuditRankItem{Key: key, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count == items[j].Count {
			return items[i].Key < items[j].Key
		}
		return items[i].Count > items[j].Count
	})
	if len(items) > limit {
		return items[:limit]
	}
	return items
}
