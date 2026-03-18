package coremain

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const defaultAuditSearchWindow = 24 * time.Hour

type AuditTextMatchMode string

const (
	AuditMatchExact AuditTextMatchMode = "exact"
	AuditMatchFuzzy AuditTextMatchMode = "fuzzy"
)

type AuditSearchField string

const (
	AuditSearchFieldQueryName    AuditSearchField = "query_name"
	AuditSearchFieldClientIP     AuditSearchField = "client_ip"
	AuditSearchFieldTraceID      AuditSearchField = "trace_id"
	AuditSearchFieldDomainSet    AuditSearchField = "domain_set"
	AuditSearchFieldAnswer       AuditSearchField = "answer"
	AuditSearchFieldQueryType    AuditSearchField = "query_type"
	AuditSearchFieldQueryClass   AuditSearchField = "query_class"
	AuditSearchFieldResponseCode AuditSearchField = "response_code"
	AuditSearchFieldUpstreamTag  AuditSearchField = "upstream_tag"
	AuditSearchFieldTransport    AuditSearchField = "transport"
	AuditSearchFieldServerName   AuditSearchField = "server_name"
	AuditSearchFieldURLPath      AuditSearchField = "url_path"
	AuditSearchFieldCacheStatus  AuditSearchField = "cache_status"
)

type AuditTimeInput struct {
	value time.Time
	set   bool
}

func (t *AuditTimeInput) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "null" {
		*t = AuditTimeInput{}
		return nil
	}
	var text string
	if raw[0] == '"' {
		if err := json.Unmarshal(data, &text); err != nil {
			return fmt.Errorf("decode audit time string: %w", err)
		}
	} else {
		text = raw
	}
	value, err := parseAuditTimeValue(text)
	if err != nil {
		return err
	}
	t.value = value
	t.set = true
	return nil
}

func (t AuditTimeInput) IsSet() bool {
	return t.set && !t.value.IsZero()
}

func (t AuditTimeInput) Time() time.Time {
	return t.value
}

type AuditTextFilter struct {
	Value string             `json:"value,omitempty"`
	Mode  AuditTextMatchMode `json:"mode,omitempty"`
}

func (f AuditTextFilter) IsZero() bool {
	return strings.TrimSpace(f.Value) == ""
}

type AuditLogKeywordSearch struct {
	Value  string             `json:"value"`
	Mode   AuditTextMatchMode `json:"mode,omitempty"`
	Fields []AuditSearchField `json:"fields,omitempty"`
}

func (s AuditLogKeywordSearch) IsZero() bool {
	return strings.TrimSpace(s.Value) == ""
}

type AuditLogSearchTimeRange struct {
	From AuditTimeInput `json:"from,omitempty"`
	To   AuditTimeInput `json:"to,omitempty"`
}

type AuditLogSearchPage struct {
	Limit  int    `json:"limit,omitempty"`
	Cursor string `json:"cursor,omitempty"`
}

type AuditLogSearchFilters struct {
	Domain        AuditTextFilter `json:"domain,omitempty"`
	ClientIP      AuditTextFilter `json:"client_ip,omitempty"`
	TraceID       AuditTextFilter `json:"trace_id,omitempty"`
	DomainSet     AuditTextFilter `json:"domain_set,omitempty"`
	Answer        AuditTextFilter `json:"answer,omitempty"`
	UpstreamTag   AuditTextFilter `json:"upstream_tag,omitempty"`
	ServerName    AuditTextFilter `json:"server_name,omitempty"`
	URLPath       AuditTextFilter `json:"url_path,omitempty"`
	QueryType     string          `json:"query_type,omitempty"`
	QueryClass    string          `json:"query_class,omitempty"`
	ResponseCode  string          `json:"response_code,omitempty"`
	CacheStatus   string          `json:"cache_status,omitempty"`
	Transport     string          `json:"transport,omitempty"`
	HasAnswer     *bool           `json:"has_answer,omitempty"`
	DurationMsMin *float64        `json:"duration_ms_min,omitempty"`
	DurationMsMax *float64        `json:"duration_ms_max,omitempty"`
}

type AuditLogSearchRequest struct {
	TimeRange AuditLogSearchTimeRange `json:"time_range,omitempty"`
	Page      AuditLogSearchPage      `json:"page,omitempty"`
	Keyword   *AuditLogKeywordSearch  `json:"keyword,omitempty"`
	Filters   AuditLogSearchFilters   `json:"filters,omitempty"`
}

type AuditLogsQuery struct {
	From    time.Time
	To      time.Time
	Limit   int
	Cursor  string
	Keyword AuditLogKeywordSearch
	Filters AuditLogSearchFilters
}

func normalizeAuditTextFilter(filter AuditTextFilter) (AuditTextFilter, error) {
	filter.Value = strings.TrimSpace(filter.Value)
	if filter.Value == "" {
		return AuditTextFilter{}, nil
	}
	mode, err := normalizeAuditMatchMode(filter.Mode, AuditMatchExact)
	if err != nil {
		return AuditTextFilter{}, err
	}
	filter.Mode = mode
	return filter, nil
}

func normalizeAuditMatchMode(mode, defaultMode AuditTextMatchMode) (AuditTextMatchMode, error) {
	if mode == "" {
		return defaultMode, nil
	}
	switch mode {
	case AuditMatchExact, AuditMatchFuzzy:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported match mode: %s", mode)
	}
}

func normalizeAuditKeywordSearch(keyword *AuditLogKeywordSearch, fallbackFields []AuditSearchField) (AuditLogKeywordSearch, error) {
	if keyword == nil {
		return AuditLogKeywordSearch{}, nil
	}
	normalized := AuditLogKeywordSearch{
		Value: strings.TrimSpace(keyword.Value),
	}
	if normalized.Value == "" {
		return AuditLogKeywordSearch{}, nil
	}
	mode, err := normalizeAuditMatchMode(keyword.Mode, AuditMatchFuzzy)
	if err != nil {
		return AuditLogKeywordSearch{}, err
	}
	fields := keyword.Fields
	if len(fields) == 0 {
		fields = fallbackFields
	}
	fields, err = normalizeAuditSearchFields(fields)
	if err != nil {
		return AuditLogKeywordSearch{}, err
	}
	normalized.Mode = mode
	normalized.Fields = fields
	return normalized, nil
}

func normalizeAuditSearchFields(fields []AuditSearchField) ([]AuditSearchField, error) {
	seen := make(map[AuditSearchField]struct{}, len(fields))
	out := make([]AuditSearchField, 0, len(fields))
	for _, field := range fields {
		switch field {
		case AuditSearchFieldQueryName,
			AuditSearchFieldClientIP,
			AuditSearchFieldTraceID,
			AuditSearchFieldDomainSet,
			AuditSearchFieldAnswer,
			AuditSearchFieldQueryType,
			AuditSearchFieldQueryClass,
			AuditSearchFieldResponseCode,
			AuditSearchFieldUpstreamTag,
			AuditSearchFieldTransport,
			AuditSearchFieldServerName,
			AuditSearchFieldURLPath,
			AuditSearchFieldCacheStatus:
		default:
			return nil, fmt.Errorf("unsupported search field: %s", field)
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	return out, nil
}

func normalizeAuditSearchFilters(filters AuditLogSearchFilters) (AuditLogSearchFilters, error) {
	var err error
	filters.Domain, err = normalizeAuditTextFilter(filters.Domain)
	if err != nil {
		return AuditLogSearchFilters{}, err
	}
	filters.ClientIP, err = normalizeAuditTextFilter(filters.ClientIP)
	if err != nil {
		return AuditLogSearchFilters{}, err
	}
	filters.TraceID, err = normalizeAuditTextFilter(filters.TraceID)
	if err != nil {
		return AuditLogSearchFilters{}, err
	}
	filters.DomainSet, err = normalizeAuditTextFilter(filters.DomainSet)
	if err != nil {
		return AuditLogSearchFilters{}, err
	}
	filters.Answer, err = normalizeAuditTextFilter(filters.Answer)
	if err != nil {
		return AuditLogSearchFilters{}, err
	}
	filters.UpstreamTag, err = normalizeAuditTextFilter(filters.UpstreamTag)
	if err != nil {
		return AuditLogSearchFilters{}, err
	}
	filters.ServerName, err = normalizeAuditTextFilter(filters.ServerName)
	if err != nil {
		return AuditLogSearchFilters{}, err
	}
	filters.URLPath, err = normalizeAuditTextFilter(filters.URLPath)
	if err != nil {
		return AuditLogSearchFilters{}, err
	}
	filters.QueryType = strings.ToUpper(strings.TrimSpace(filters.QueryType))
	filters.QueryClass = strings.ToUpper(strings.TrimSpace(filters.QueryClass))
	filters.ResponseCode = strings.ToUpper(strings.TrimSpace(filters.ResponseCode))
	filters.CacheStatus = strings.TrimSpace(filters.CacheStatus)
	filters.Transport = strings.TrimSpace(filters.Transport)
	if filters.DurationMsMin != nil && filters.DurationMsMax != nil && *filters.DurationMsMin > *filters.DurationMsMax {
		return AuditLogSearchFilters{}, fmt.Errorf("duration_ms_min must be less than or equal to duration_ms_max")
	}
	return filters, nil
}

func allAuditSearchFields() []AuditSearchField {
	return []AuditSearchField{
		AuditSearchFieldQueryName,
		AuditSearchFieldClientIP,
		AuditSearchFieldTraceID,
		AuditSearchFieldDomainSet,
		AuditSearchFieldAnswer,
		AuditSearchFieldQueryType,
		AuditSearchFieldQueryClass,
		AuditSearchFieldResponseCode,
		AuditSearchFieldUpstreamTag,
		AuditSearchFieldTransport,
		AuditSearchFieldServerName,
		AuditSearchFieldURLPath,
		AuditSearchFieldCacheStatus,
	}
}

func legacyAuditSearchFields() []AuditSearchField {
	return []AuditSearchField{
		AuditSearchFieldQueryName,
		AuditSearchFieldClientIP,
		AuditSearchFieldTraceID,
		AuditSearchFieldDomainSet,
		AuditSearchFieldAnswer,
	}
}

func newAuditMillisInput(value int64) AuditTimeInput {
	return AuditTimeInput{value: time.UnixMilli(value), set: true}
}

func parseAuditMillisString(value string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
}
