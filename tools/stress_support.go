package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/miekg/dns"
)

type latencySummary struct {
	AvgMs float64 `json:"avg_ms"`
	P50Ms float64 `json:"p50_ms"`
	P90Ms float64 `json:"p90_ms"`
	P95Ms float64 `json:"p95_ms"`
	P99Ms float64 `json:"p99_ms"`
	MaxMs float64 `json:"max_ms"`
}

type failureSample struct {
	Phase     string  `json:"phase"`
	Network   string  `json:"network"`
	Domain    string  `json:"domain"`
	QType     string  `json:"qtype"`
	Error     string  `json:"error,omitempty"`
	RCode     string  `json:"rcode,omitempty"`
	LatencyMs float64 `json:"latency_ms"`
}

type phaseResult struct {
	Phase         string          `json:"phase"`
	Network       string          `json:"network"`
	QType         string          `json:"qtype"`
	Total         int             `json:"total"`
	Responses     int             `json:"responses"`
	ResponseRate  float64         `json:"response_rate"`
	NoError       int             `json:"noerror"`
	NXDomain      int             `json:"nxdomain"`
	ServFail      int             `json:"servfail"`
	Timeouts      int             `json:"timeouts"`
	Errors        int             `json:"errors"`
	OtherRCodes   map[string]int  `json:"other_rcodes,omitempty"`
	Duration      string          `json:"duration"`
	DurationMs    float64         `json:"duration_ms"`
	SentQPS       float64         `json:"sent_qps"`
	ResponseQPS   float64         `json:"response_qps"`
	Latency       latencySummary  `json:"latency"`
	Breakdown     phaseBreakdown  `json:"breakdown"`
	FailureSample []failureSample `json:"failure_samples,omitempty"`
}

type phaseBreakdown struct {
	PositiveResponses   int     `json:"positive_responses"`
	PositiveRate        float64 `json:"positive_rate"`
	NXDomainResponses   int     `json:"nxdomain_responses"`
	NXDomainRate        float64 `json:"nxdomain_rate"`
	ServFailResponses   int     `json:"servfail_responses"`
	ServFailRate        float64 `json:"servfail_rate"`
	OtherRCodeResponses int     `json:"other_rcode_responses"`
	OtherRCodeRate      float64 `json:"other_rcode_rate"`
	TimeoutErrors       int     `json:"timeout_errors"`
	TimeoutRate         float64 `json:"timeout_rate"`
	TransportErrors     int     `json:"transport_errors"`
	TransportErrorRate  float64 `json:"transport_error_rate"`
}

type cacheEffectSummary struct {
	ColdPhase          string  `json:"cold_phase"`
	RepeatPhase        string  `json:"repeat_phase"`
	ColdPositiveRate   float64 `json:"cold_positive_rate"`
	RepeatPositiveRate float64 `json:"repeat_positive_rate"`
	PositiveRateDelta  float64 `json:"positive_rate_delta"`
	ColdTimeoutRate    float64 `json:"cold_timeout_rate"`
	RepeatTimeoutRate  float64 `json:"repeat_timeout_rate"`
	TimeoutRateDelta   float64 `json:"timeout_rate_delta"`
	ColdP50Ms          float64 `json:"cold_p50_ms"`
	RepeatP50Ms        float64 `json:"repeat_p50_ms"`
	P50DeltaMs         float64 `json:"p50_delta_ms"`
	ColdP95Ms          float64 `json:"cold_p95_ms"`
	RepeatP95Ms        float64 `json:"repeat_p95_ms"`
	P95DeltaMs         float64 `json:"p95_delta_ms"`
}

type dnsStressReport struct {
	GeneratedAt           string              `json:"generated_at"`
	TargetServer          string              `json:"target_server"`
	QuestionType          string              `json:"question_type"`
	RequestedTotalQueries int                 `json:"requested_total_queries"`
	RequestedUnique       int                 `json:"requested_unique_domains"`
	UniqueDomains         int                 `json:"unique_domains"`
	RepeatedQueries       int                 `json:"repeated_queries"`
	HotsetSize            int                 `json:"hotset_size"`
	Concurrency           int                 `json:"concurrency"`
	QPS                   int                 `json:"qps"`
	TCPSample             int                 `json:"tcp_sample"`
	Source                map[string]any      `json:"source"`
	CacheEffect           *cacheEffectSummary `json:"cache_effect,omitempty"`
	Phases                []phaseResult       `json:"phases"`
	GeneratedDomains      []string            `json:"generated_domains_preview,omitempty"`
}

type trafficPlan struct {
	ColdQuestions   []stressQuestion
	RepeatQuestions []stressQuestion
	HotsetSize      int
}

func buildDomainCorpus(ctx context.Context, httpClient *http.Client, path string, sourceURLs []string, count int, allowGenerated bool) ([]string, []string, map[string]any, error) {
	seen := make(map[string]struct{}, count)
	domains := make([]string, 0, count)
	loadedCount := 0
	sourceInfo := map[string]any{"requested": count, "file": path, "source_urls": sourceURLs}
	var sourceLines int

	if path != "" {
		file, err := os.Open(path)
		if err != nil {
			return nil, nil, nil, err
		}
		defer file.Close()
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() && len(domains) < count {
			sourceLines++
			domain, ok := parseDomainLine(scanner.Text())
			if !ok {
				continue
			}
			if _, exists := seen[domain]; exists {
				continue
			}
			seen[domain] = struct{}{}
			domains = append(domains, domain)
		}
		if err := scanner.Err(); err != nil {
			return nil, nil, nil, err
		}
		loadedCount = len(domains)
	}

	remoteLoaded := 0
	for _, rawURL := range sourceURLs {
		url := strings.TrimSpace(rawURL)
		if url == "" || len(domains) >= count {
			continue
		}
		items, lines, err := fetchRemoteDomains(ctx, httpClient, url, count-len(domains))
		if err != nil {
			return nil, nil, nil, err
		}
		sourceLines += lines
		for _, domain := range items {
			if _, exists := seen[domain]; exists {
				continue
			}
			seen[domain] = struct{}{}
			domains = append(domains, domain)
			remoteLoaded++
			if len(domains) >= count {
				break
			}
		}
	}

	if allowGenerated {
		seedBases := make([]string, 0, maxInt(1, len(domains)))
		for _, domain := range domains {
			seedBases = append(seedBases, strings.TrimSuffix(domain, "."))
		}
		if len(seedBases) == 0 {
			seedBases = []string{"example.com", "example.net", "example.org", "iana.org", "example.edu"}
		}
		for len(domains) < count {
			seed := seedBases[len(domains)%len(seedBases)]
			candidate := dns.Fqdn(fmt.Sprintf("ha-%06d.%s", len(domains)+1, seed))
			if _, exists := seen[candidate]; exists {
				continue
			}
			seen[candidate] = struct{}{}
			domains = append(domains, candidate)
		}
	}

	actualUnique := len(domains)
	sourceInfo["source_lines"] = sourceLines
	sourceInfo["actual_unique"] = actualUnique
	sourceInfo["unique_loaded"] = loadedCount
	sourceInfo["remote_loaded"] = remoteLoaded
	sourceInfo["generated"] = len(domains) - loadedCount - remoteLoaded
	sourceInfo["allow_generated"] = allowGenerated
	sourceInfo["insufficient_real_domains"] = actualUnique < count
	preview := append([]string(nil), domains[:minInt(10, len(domains))]...)
	return domains, preview, sourceInfo, nil
}

func buildTrafficPlan(domains []string, totalQueries int, qtype uint16, hotsetRatio float64) trafficPlan {
	actualUnique := minInt(len(domains), totalQueries)
	cold := make([]stressQuestion, 0, actualUnique)
	for _, domain := range domains[:actualUnique] {
		cold = append(cold, stressQuestion{Name: domain, QType: qtype})
	}

	repeats := totalQueries - actualUnique
	if repeats <= 0 {
		return trafficPlan{ColdQuestions: cold, HotsetSize: actualUnique}
	}

	hotsetSize := maxInt(1, int(math.Ceil(float64(actualUnique)*hotsetRatio)))
	if hotsetSize > actualUnique {
		hotsetSize = actualUnique
	}
	zipfRand := rand.New(rand.NewSource(1))
	zipf := rand.NewZipf(zipfRand, 1.15, 3, uint64(hotsetSize-1))
	replay := make([]stressQuestion, 0, repeats)
	for i := 0; i < repeats; i++ {
		idx := int(zipf.Uint64())
		replay = append(replay, stressQuestion{Name: domains[idx], QType: qtype})
	}
	return trafficPlan{
		ColdQuestions:   cold,
		RepeatQuestions: replay,
		HotsetSize:      hotsetSize,
	}
}

func fetchRemoteDomains(ctx context.Context, httpClient *http.Client, url string, limit int) ([]string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, 0, fmt.Errorf("GET %s returned %s", url, resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	domains := make([]string, 0, minInt(limit, 4096))
	lines := 0
	for scanner.Scan() && len(domains) < limit {
		lines++
		domain, ok := parseDomainLine(scanner.Text())
		if !ok {
			continue
		}
		domains = append(domains, domain)
	}
	if err := scanner.Err(); err != nil {
		return nil, lines, err
	}
	return domains, lines, nil
}

func parseDomainLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	fields := strings.Fields(trimmed)
	for i := len(fields) - 1; i >= 0; i-- {
		token := normalizeRuleToken(fields[i])
		token = strings.TrimSuffix(token, ".")
		if !looksLikeDomain(token) {
			continue
		}
		return dns.Fqdn(strings.ToLower(token)), true
	}
	return "", false
}

func normalizeRuleToken(token string) string {
	token = strings.Trim(strings.TrimSpace(token), "[](),;\"'")
	if token == "" {
		return ""
	}
	lower := strings.ToLower(token)
	switch {
	case strings.HasPrefix(lower, "+."):
		return token[2:]
	case strings.HasPrefix(lower, "full:"):
		return token[5:]
	case strings.HasPrefix(lower, "domain:"):
		return token[7:]
	case strings.HasPrefix(lower, "."):
		return token[1:]
	case strings.HasPrefix(lower, "keyword:"), strings.HasPrefix(lower, "regexp:"), strings.HasPrefix(lower, "geosite:"):
		return ""
	default:
		return token
	}
}

func looksLikeDomain(token string) bool {
	if token == "" || strings.ContainsAny(token, "/@:[]") {
		return false
	}
	if net.ParseIP(token) != nil || !strings.Contains(token, ".") {
		return false
	}
	for _, part := range strings.Split(token, ".") {
		if part == "" {
			return false
		}
	}
	return true
}

func summarizeLatencies(values []float64) latencySummary {
	if len(values) == 0 {
		return latencySummary{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	return latencySummary{
		AvgMs: roundFloat(sum/float64(len(sorted)), 3),
		P50Ms: roundFloat(percentile(sorted, 0.50), 3),
		P90Ms: roundFloat(percentile(sorted, 0.90), 3),
		P95Ms: roundFloat(percentile(sorted, 0.95), 3),
		P99Ms: roundFloat(percentile(sorted, 0.99), 3),
		MaxMs: roundFloat(sorted[len(sorted)-1], 3),
	}
}

func percentile(sorted []float64, ratio float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if ratio <= 0 {
		return sorted[0]
	}
	if ratio >= 1 {
		return sorted[len(sorted)-1]
	}
	idx := int(math.Ceil(float64(len(sorted))*ratio)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func buildOtherRCodes(rcodeCounts map[string]int) map[string]int {
	other := map[string]int{}
	for name, count := range rcodeCounts {
		if name == dns.RcodeToString[dns.RcodeSuccess] || name == dns.RcodeToString[dns.RcodeNameError] || name == dns.RcodeToString[dns.RcodeServerFailure] {
			continue
		}
		other[name] = count
	}
	if len(other) == 0 {
		return nil
	}
	return other
}

func buildPhaseBreakdown(total int, rcodeCounts map[string]int, timeouts int, errors int) phaseBreakdown {
	otherCount := 0
	for name, count := range buildOtherRCodes(rcodeCounts) {
		if name == "" {
			continue
		}
		otherCount += count
	}
	return phaseBreakdown{
		PositiveResponses:   rcodeCounts[dns.RcodeToString[dns.RcodeSuccess]],
		PositiveRate:        safeRate(rcodeCounts[dns.RcodeToString[dns.RcodeSuccess]], total),
		NXDomainResponses:   rcodeCounts[dns.RcodeToString[dns.RcodeNameError]],
		NXDomainRate:        safeRate(rcodeCounts[dns.RcodeToString[dns.RcodeNameError]], total),
		ServFailResponses:   rcodeCounts[dns.RcodeToString[dns.RcodeServerFailure]],
		ServFailRate:        safeRate(rcodeCounts[dns.RcodeToString[dns.RcodeServerFailure]], total),
		OtherRCodeResponses: otherCount,
		OtherRCodeRate:      safeRate(otherCount, total),
		TimeoutErrors:       timeouts,
		TimeoutRate:         safeRate(timeouts, total),
		TransportErrors:     errors,
		TransportErrorRate:  safeRate(errors, total),
	}
}

func buildCacheEffectSummary(cold phaseResult, repeat phaseResult) *cacheEffectSummary {
	return &cacheEffectSummary{
		ColdPhase:          cold.Phase,
		RepeatPhase:        repeat.Phase,
		ColdPositiveRate:   cold.Breakdown.PositiveRate,
		RepeatPositiveRate: repeat.Breakdown.PositiveRate,
		PositiveRateDelta:  roundFloat(repeat.Breakdown.PositiveRate-cold.Breakdown.PositiveRate, 6),
		ColdTimeoutRate:    cold.Breakdown.TimeoutRate,
		RepeatTimeoutRate:  repeat.Breakdown.TimeoutRate,
		TimeoutRateDelta:   roundFloat(repeat.Breakdown.TimeoutRate-cold.Breakdown.TimeoutRate, 6),
		ColdP50Ms:          cold.Latency.P50Ms,
		RepeatP50Ms:        repeat.Latency.P50Ms,
		P50DeltaMs:         roundFloat(repeat.Latency.P50Ms-cold.Latency.P50Ms, 3),
		ColdP95Ms:          cold.Latency.P95Ms,
		RepeatP95Ms:        repeat.Latency.P95Ms,
		P95DeltaMs:         roundFloat(repeat.Latency.P95Ms-cold.Latency.P95Ms, 3),
	}
}

func parseQType(value string) (uint16, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "A":
		return dns.TypeA, nil
	case "AAAA":
		return dns.TypeAAAA, nil
	default:
		return 0, fmt.Errorf("unsupported qtype %q, only A and AAAA are supported", value)
	}
}

func writeJSONFile(path string, data any) error {
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, payload, 0o644)
}

func writeFailureSamples(path string, samples []failureSample) error {
	var builder strings.Builder
	for _, sample := range samples {
		payload, err := json.Marshal(sample)
		if err != nil {
			return err
		}
		builder.Write(payload)
		builder.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(builder.String()), 0o644)
}

func isTimeoutErr(err error) bool {
	var netErr net.Error
	return err != nil && errors.As(err, &netErr) && netErr.Timeout()
}

func roundFloat(value float64, precision int) float64 {
	pow := math.Pow10(precision)
	return math.Round(value*pow) / pow
}

func safeRate(numerator int, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return roundFloat(float64(numerator)/float64(denominator), 6)
}

func safePerSecond(value int, seconds float64) float64 {
	if seconds <= 0 {
		return 0
	}
	return roundFloat(float64(value)/seconds, 3)
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
