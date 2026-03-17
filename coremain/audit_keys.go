package coremain

import "github.com/IrineSistiana/mosdns/v5/pkg/query_context"

var (
	auditCacheStatusKey = query_context.RegKey()
	auditUpstreamTagKey = query_context.RegKey()
)

const (
	AuditCacheBypass = "bypass"
	AuditCacheMiss   = "miss"
	AuditCacheHit    = "hit"
	AuditCacheLazy   = "lazy_hit"
)

func SetAuditCacheStatus(qCtx *query_context.Context, status string) {
	if qCtx == nil || status == "" {
		return
	}
	qCtx.StoreValue(auditCacheStatusKey, status)
}

func SetAuditUpstreamTag(qCtx *query_context.Context, tag string) {
	if qCtx == nil || tag == "" {
		return
	}
	qCtx.StoreValue(auditUpstreamTagKey, tag)
}

func getAuditCacheStatus(qCtx *query_context.Context) string {
	if qCtx == nil {
		return AuditCacheBypass
	}
	value, ok := qCtx.GetValue(auditCacheStatusKey)
	if !ok {
		return AuditCacheBypass
	}
	status, _ := value.(string)
	if status == "" {
		return AuditCacheBypass
	}
	return status
}

func getAuditUpstreamTag(qCtx *query_context.Context) string {
	if qCtx == nil {
		return ""
	}
	value, ok := qCtx.GetValue(auditUpstreamTagKey)
	if !ok {
		return ""
	}
	tag, _ := value.(string)
	return tag
}
