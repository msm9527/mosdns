package coremain

import "strings"

type auditTextColumn struct {
	column       string
	exactSetLike bool
}

func buildAuditLogWhere(params AuditLogsQuery) ([]string, []any) {
	builder := auditWhereBuilder{
		where: []string{"query_time_unix_ms BETWEEN ? AND ?"},
		args:  []any{params.From.UnixMilli(), params.To.UnixMilli()},
	}
	builder.addKeyword(params.Keyword)
	builder.addTextFilter(auditTextColumn{column: "query_name"}, params.Filters.Domain)
	builder.addTextFilter(auditTextColumn{column: "client_ip"}, params.Filters.ClientIP)
	builder.addTextFilter(auditTextColumn{column: "trace_id"}, params.Filters.TraceID)
	builder.addTextFilter(auditTextColumn{column: "domain_set_norm"}, params.Filters.DomainSet)
	builder.addTextFilter(auditTextColumn{column: "answer_search_text", exactSetLike: true}, params.Filters.Answer)
	builder.addTextFilter(auditTextColumn{column: "upstream_tag"}, params.Filters.UpstreamTag)
	builder.addTextFilter(auditTextColumn{column: "server_name"}, params.Filters.ServerName)
	builder.addTextFilter(auditTextColumn{column: "url_path"}, params.Filters.URLPath)
	builder.addExactFilter("query_type", params.Filters.QueryType)
	builder.addExactFilter("query_class", params.Filters.QueryClass)
	builder.addExactFilter("response_code", params.Filters.ResponseCode)
	builder.addExactFilter("cache_status", params.Filters.CacheStatus)
	builder.addExactFilter("transport", params.Filters.Transport)
	builder.addAnswerState(params.Filters.HasAnswer)
	builder.addDurationRange(params.Filters.DurationMsMin, params.Filters.DurationMsMax)
	return builder.where, builder.args
}

type auditWhereBuilder struct {
	where []string
	args  []any
}

func (b *auditWhereBuilder) addClause(clause string, args ...any) {
	if clause == "" {
		return
	}
	b.where = append(b.where, clause)
	b.args = append(b.args, args...)
}

func (b *auditWhereBuilder) addKeyword(keyword AuditLogKeywordSearch) {
	if keyword.IsZero() {
		return
	}
	clauses := make([]string, 0, len(keyword.Fields))
	args := make([]any, 0, len(keyword.Fields))
	for _, field := range keyword.Fields {
		spec, ok := auditSearchFieldColumn(field)
		if !ok {
			continue
		}
		clause, value := buildAuditTextClause(spec, AuditTextFilter{
			Value: keyword.Value,
			Mode:  keyword.Mode,
		})
		clauses = append(clauses, clause)
		args = append(args, value)
	}
	if len(clauses) == 0 {
		return
	}
	b.addClause("("+strings.Join(clauses, " OR ")+")", args...)
}

func (b *auditWhereBuilder) addTextFilter(column auditTextColumn, filter AuditTextFilter) {
	if filter.IsZero() {
		return
	}
	clause, value := buildAuditTextClause(column, filter)
	b.addClause(clause, value)
}

func (b *auditWhereBuilder) addExactFilter(column, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	b.addClause(column+" = ?", value)
}

func (b *auditWhereBuilder) addAnswerState(hasAnswer *bool) {
	if hasAnswer == nil {
		return
	}
	if *hasAnswer {
		b.addClause("answer_count > 0")
		return
	}
	b.addClause("answer_count = 0")
}

func (b *auditWhereBuilder) addDurationRange(min, max *float64) {
	if min != nil {
		b.addClause("duration_ms >= ?", *min)
	}
	if max != nil {
		b.addClause("duration_ms <= ?", *max)
	}
}

func buildAuditTextClause(column auditTextColumn, filter AuditTextFilter) (string, any) {
	if column.exactSetLike && filter.Mode == AuditMatchExact {
		return column.column + " LIKE ?", wrapExactPattern(filter.Value)
	}
	if filter.Mode == AuditMatchExact {
		return column.column + " = ?", filter.Value
	}
	return column.column + " LIKE ? COLLATE NOCASE", "%" + filter.Value + "%"
}

func auditSearchFieldColumn(field AuditSearchField) (auditTextColumn, bool) {
	switch field {
	case AuditSearchFieldQueryName:
		return auditTextColumn{column: "query_name"}, true
	case AuditSearchFieldClientIP:
		return auditTextColumn{column: "client_ip"}, true
	case AuditSearchFieldTraceID:
		return auditTextColumn{column: "trace_id"}, true
	case AuditSearchFieldDomainSet:
		return auditTextColumn{column: "domain_set_norm"}, true
	case AuditSearchFieldAnswer:
		return auditTextColumn{column: "answer_search_text", exactSetLike: true}, true
	case AuditSearchFieldQueryType:
		return auditTextColumn{column: "query_type"}, true
	case AuditSearchFieldQueryClass:
		return auditTextColumn{column: "query_class"}, true
	case AuditSearchFieldResponseCode:
		return auditTextColumn{column: "response_code"}, true
	case AuditSearchFieldUpstreamTag:
		return auditTextColumn{column: "upstream_tag"}, true
	case AuditSearchFieldTransport:
		return auditTextColumn{column: "transport"}, true
	case AuditSearchFieldServerName:
		return auditTextColumn{column: "server_name"}, true
	case AuditSearchFieldURLPath:
		return auditTextColumn{column: "url_path"}, true
	case AuditSearchFieldCacheStatus:
		return auditTextColumn{column: "cache_status"}, true
	default:
		return auditTextColumn{}, false
	}
}
