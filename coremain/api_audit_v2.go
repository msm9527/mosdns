package coremain

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// --- V2 API Data Structures ---

// V2StatsResponse for API: /api/v2/audit/stats
type V2StatsResponse struct {
	TotalQueries      uint64  `json:"total_queries"` // MODIFIED: Changed from int to uint64
	AverageDurationMs float64 `json:"average_duration_ms"`
}

// V2RankItem for ranking APIs
type V2RankItem struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// V2PaginatedLogsResponse for API: /api/v2/audit/logs
type V2PaginatedLogsResponse struct {
	Pagination V2PaginationInfo `json:"pagination"`
	Logs       []AuditLog       `json:"logs"`
}

type V2PaginationInfo struct {
	TotalItems   int `json:"total_items"`
	TotalPages   int `json:"total_pages"`
	CurrentPage  int `json:"current_page"`
	ItemsPerPage int `json:"items_per_page"`
}

// RegisterAuditAPIV2 registers all new v2 audit log APIs.
// This function is completely separate from the v1 registration.
func RegisterAuditAPIV2(router *chi.Mux) {
	router.Route("/api/v2/audit", func(r chi.Router) {
		// 高频读接口不再绑定 WithAsyncGC，避免请求风暴触发 GC 抖动。
		r.Get("/stats", handleV2GetStats)                   // 概览页调用
		r.Get("/rank/domain", handleV2GetDomainRank)        // 概览页调用
		r.Get("/rank/client", handleV2GetClientRank)        // 概览页调用
		r.Get("/rank/domain_set", handleV2GetDomainSetRank) // 概览页调用
		r.Get("/rank/slowest", handleV2GetSlowestQueries)   // 概览页调用
		r.Get("/logs", handleV2GetLogs)
	})
}

// --- V2 API Handlers ---

// 1. Handler for: Get total queries and average duration
func handleV2GetStats(w http.ResponseWriter, r *http.Request) {
	stats := GlobalAuditCollector.CalculateV2Stats()
	writeJSON(w, http.StatusOK, stats)
}

// 2. Handler for: Get domain query ranking
func handleV2GetDomainRank(w http.ResponseWriter, r *http.Request) {
	limit := parseQueryInt(r, "limit", 20) // Default to top 20
	// MODIFIED: Use the new RankByDomain enum
	rank := GlobalAuditCollector.CalculateRank(RankByDomain, limit) // Changed function argument
	writeJSON(w, http.StatusOK, rank)
}

// 3. Handler for: Get client IP query ranking
func handleV2GetClientRank(w http.ResponseWriter, r *http.Request) {
	limit := parseQueryInt(r, "limit", 20) // Default to top 20
	// MODIFIED: Use the new RankByClient enum
	rank := GlobalAuditCollector.CalculateRank(RankByClient, limit) // Changed function argument
	writeJSON(w, http.StatusOK, rank)
}

// --- ADDED START ---
// 4. Handler for: Get domain_set query ranking
func handleV2GetDomainSetRank(w http.ResponseWriter, r *http.Request) {
	limit := parseQueryInt(r, "limit", 20) // Default to top 20
	rank := GlobalAuditCollector.CalculateRank(RankByDomainSet, limit)
	writeJSON(w, http.StatusOK, rank)
}

// --- ADDED END ---

// 5. Handler for: Get slowest queries
func handleV2GetSlowestQueries(w http.ResponseWriter, r *http.Request) {
	limit := parseQueryInt(r, "limit", 100) // Default to 100 slowest
	logs := GlobalAuditCollector.GetSlowestQueries(limit)
	writeJSON(w, http.StatusOK, logs)
}

// 6. Handler for: Get logs with advanced filtering and pagination
func handleV2GetLogs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	exactSearch, _ := strconv.ParseBool(query.Get("exact"))

	params := V2GetLogsParams{
		Page:        parseQueryInt(r, "page", 1),
		Limit:       parseQueryInt(r, "limit", 50),
		Domain:      query.Get("domain"),
		AnswerIP:    query.Get("answer_ip"),
		AnswerCNAME: query.Get("cname"),
		ClientIP:    query.Get("client_ip"),
		Q:           query.Get("q"),
		Exact:       exactSearch,
	}

	response := GlobalAuditCollector.GetV2Logs(params)
	writeJSON(w, http.StatusOK, response)
}

// Helper function to parse integer from query string with a default value.
func parseQueryInt(r *http.Request, key string, defaultValue int) int {
	if valueStr := r.URL.Query().Get(key); valueStr != "" {
		if value, err := strconv.Atoi(valueStr); err == nil && value > 0 {
			return value
		}
	}
	return defaultValue
}
