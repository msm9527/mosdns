package coremain

import (
	"testing"
	"time"
)

func TestNormalizeAuditDomainSet(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		qtype string
		want  string
	}{
		{
			name:  "memory proxy wins for A query",
			raw:   "记忆代理|记忆无V6|订阅代理",
			qtype: "A",
			want:  "记忆代理",
		},
		{
			name:  "memory no v6 wins for AAAA query",
			raw:   "记忆无V6|记忆直连|订阅直连",
			qtype: "AAAA",
			want:  "记忆无V6",
		},
		{
			name:  "unmatched alias is normalized",
			raw:   "unmatched_rule",
			qtype: "A",
			want:  unmatchedAuditDomainSet,
		},
		{
			name:  "unknown combo falls back to first tag",
			raw:   "自定义规则|另一个规则",
			qtype: "A",
			want:  "自定义规则",
		},
		{
			name:  "duplicate tags are collapsed",
			raw:   "记忆代理|记忆代理|订阅代理",
			qtype: "A",
			want:  "记忆代理",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeAuditDomainSet(tt.raw, tt.qtype); got != tt.want {
				t.Fatalf("normalizeAuditDomainSet() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAuditCollectorCalculateRankByDomainSetNormalizes(t *testing.T) {
	c := NewAuditCollector(AuditSettings{
		MemoryEntries: 8,
		RetentionDays: 7,
		MaxDiskSizeMB: 32,
	}, "")

	now := time.Now()
	logs := []AuditLog{
		{QueryName: "one.example", QueryType: "A", QueryTime: now, DomainSet: "记忆代理|记忆无V6|订阅代理"},
		{QueryName: "two.example", QueryType: "A", QueryTime: now, DomainSet: "记忆代理|订阅代理"},
		{QueryName: "three.example", QueryType: "AAAA", QueryTime: now, DomainSet: "记忆无V6|记忆直连|订阅直连"},
		{QueryName: "four.example", QueryType: "A", QueryTime: now, DomainSet: "广告屏蔽|订阅直连"},
	}

	c.mu.Lock()
	for _, log := range logs {
		c.appendLogLocked(log)
	}
	c.mu.Unlock()

	rank := c.CalculateRank(RankByDomainSet, 10)
	got := make(map[string]int, len(rank))
	for _, item := range rank {
		got[item.Key] = item.Count
	}

	if got["记忆代理"] != 2 {
		t.Fatalf("记忆代理 count = %d, want 2", got["记忆代理"])
	}
	if got["记忆无V6"] != 1 {
		t.Fatalf("记忆无V6 count = %d, want 1", got["记忆无V6"])
	}
	if got["广告屏蔽"] != 1 {
		t.Fatalf("广告屏蔽 count = %d, want 1", got["广告屏蔽"])
	}
}
