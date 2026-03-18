package coremain

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

func handleSearchAuditLogs(w http.ResponseWriter, r *http.Request) {
	params, err := parseAuditLogSearchRequest(w, r)
	if err != nil {
		return
	}
	resp, err := GlobalAuditCollector.GetLogs(params)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "QUERY_AUDIT_LOGS_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func parseAuditLogSearchRequest(w http.ResponseWriter, r *http.Request) (AuditLogsQuery, error) {
	var req AuditLogSearchRequest
	if err := decodeJSONBodyStrict(w, r, &req, false); err != nil {
		if errors.Is(err, errJSONBodyTooLarge) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "REQUEST_BODY_TOO_LARGE", "Request body too large")
			return AuditLogsQuery{}, err
		}
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST_BODY", "Invalid request body: "+err.Error())
		return AuditLogsQuery{}, err
	}
	params, err := buildAuditLogSearchQuery(req)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_AUDIT_QUERY", err.Error())
		return AuditLogsQuery{}, err
	}
	return params, nil
}

func buildAuditLogSearchQuery(req AuditLogSearchRequest) (AuditLogsQuery, error) {
	from, to, err := resolveAuditTimeRangeValues(
		auditTimeOrZero(req.TimeRange.From),
		auditTimeOrZero(req.TimeRange.To),
		defaultAuditSearchWindow,
	)
	if err != nil {
		return AuditLogsQuery{}, err
	}
	keyword, err := normalizeAuditKeywordSearch(req.Keyword, allAuditSearchFields())
	if err != nil {
		return AuditLogsQuery{}, err
	}
	filters, err := normalizeAuditSearchFilters(req.Filters)
	if err != nil {
		return AuditLogsQuery{}, err
	}
	return AuditLogsQuery{
		From:    from,
		To:      to,
		Limit:   req.Page.Limit,
		Cursor:  strings.TrimSpace(req.Page.Cursor),
		Keyword: keyword,
		Filters: filters,
	}, nil
}

func resolveAuditTimeRangeValues(from, to time.Time, defaultWindow time.Duration) (time.Time, time.Time, error) {
	now := time.Now()
	if to.IsZero() {
		to = now
	}
	if from.IsZero() {
		from = to.Add(-defaultWindow)
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, errors.New("from must be earlier than to")
	}
	return from, to, nil
}

func auditTimeOrZero(input AuditTimeInput) time.Time {
	if !input.IsSet() {
		return time.Time{}
	}
	return input.Time()
}
