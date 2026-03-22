package e2e_test

import (
	"fmt"
	"strings"
	"time"
)

type e2eCoverageView struct {
	DNSChecks    int `json:"dns_checks"`
	APIChecks    int `json:"api_checks"`
	LoadMetrics  int `json:"load_metrics"`
	CacheMetrics int `json:"cache_metrics"`
}

type e2eSuiteView struct {
	Name          string `json:"name"`
	Category      string `json:"category"`
	Status        string `json:"status"`
	Duration      string `json:"duration"`
	DurationWidth string `json:"duration_width"`
	ChecksCount   int    `json:"checks_count"`
	MetricsCount  int    `json:"metrics_count"`
	Artifact      string `json:"artifact"`
	Detail        string `json:"detail"`
}

type e2eDNSCheckView struct {
	Case     string `json:"case"`
	Name     string `json:"name"`
	Network  string `json:"network"`
	Listener string `json:"listener"`
	Domain   string `json:"domain"`
	Query    string `json:"query"`
	Rcode    string `json:"rcode"`
	Answers  string `json:"answers"`
}

type e2eAPICheckView struct {
	Case   string `json:"case"`
	Name   string `json:"name"`
	Method string `json:"method"`
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

type e2ePerformanceView struct {
	Highlights   []e2eMetricView `json:"highlights"`
	LoadMetrics  []e2eMetricView `json:"load_metrics"`
	CacheMetrics []e2eMetricView `json:"cache_metrics"`
	OtherMetrics []e2eMetricView `json:"other_metrics"`
}

type e2eMetricView struct {
	Case   string `json:"case"`
	Name   string `json:"name"`
	Value  string `json:"value"`
	Detail string `json:"detail"`
}

func enrichE2EReportView(view e2eReportView) e2eReportView {
	view.SuccessRate = formatE2ESuccessRate(view.Passed, view.Total)
	view.Suites = buildE2ESuiteViews(view.Cases)
	view.DNSChecks = collectE2EDNSChecks(view.Cases)
	view.APIChecks = collectE2EAPIChecks(view.Cases)
	view.Performance = buildE2EPerformanceView(view.Cases)
	view.Coverage = buildE2ECoverage(view.DNSChecks, view.APIChecks, view.Performance)
	return view
}

func formatE2ESuccessRate(passed, total int) string {
	if total == 0 {
		return "0.0%"
	}
	return fmt.Sprintf("%.1f%%", float64(passed)*100/float64(total))
}

func buildE2ESuiteViews(cases []e2eCaseReport) []e2eSuiteView {
	maxDuration := maxE2EDuration(cases)
	suites := make([]e2eSuiteView, 0, len(cases))
	for _, item := range cases {
		duration := parseE2EDuration(item.Duration)
		suites = append(suites, e2eSuiteView{
			Name:          item.Name,
			Category:      e2eSuiteCategory(item.Name),
			Status:        item.Status,
			Duration:      item.Duration,
			DurationWidth: formatE2EDurationWidth(duration, maxDuration),
			ChecksCount:   len(item.Checks),
			MetricsCount:  len(item.Metrics),
			Artifact:      item.Artifact,
			Detail:        item.Detail,
		})
	}
	return suites
}

func maxE2EDuration(cases []e2eCaseReport) time.Duration {
	var maxDuration time.Duration
	for _, item := range cases {
		duration := parseE2EDuration(item.Duration)
		if duration > maxDuration {
			maxDuration = duration
		}
	}
	if maxDuration == 0 {
		return time.Millisecond
	}
	return maxDuration
}

func parseE2EDuration(value string) time.Duration {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return duration
}

func formatE2EDurationWidth(duration, maxDuration time.Duration) string {
	if maxDuration <= 0 {
		return "0%"
	}
	width := float64(duration) * 100 / float64(maxDuration)
	if width < 8 {
		width = 8
	}
	return fmt.Sprintf("%.1f%%", width)
}

func e2eSuiteCategory(name string) string {
	switch name {
	case "control api":
		return "Control Plane"
	case "udp and tcp dns", "specialized listeners":
		return "DNS Data Plane"
	case "block and ad switches", "query type and ipv6 switches", "routing mode switches":
		return "Policy Switches"
	case "rule apis":
		return "Rule Management"
	case "cache stats and stability":
		return "Cache & Load"
	default:
		return "General"
	}
}

func collectE2EDNSChecks(cases []e2eCaseReport) []e2eDNSCheckView {
	items := make([]e2eDNSCheckView, 0, 32)
	for _, item := range cases {
		for _, check := range item.Checks {
			dnsCheck, ok := parseE2EDNSCheck(item.Name, check)
			if ok {
				items = append(items, dnsCheck)
			}
		}
	}
	return items
}

func parseE2EDNSCheck(caseName string, check e2eCheckReport) (e2eDNSCheckView, bool) {
	prefix, suffix, ok := strings.Cut(check.Detail, " -> rcode=")
	if !ok {
		return e2eDNSCheckView{}, false
	}
	fields := strings.Fields(prefix)
	if len(fields) < 4 {
		return e2eDNSCheckView{}, false
	}
	rcode, answers, ok := strings.Cut(suffix, " answers=")
	if !ok {
		return e2eDNSCheckView{}, false
	}
	return e2eDNSCheckView{
		Case:     caseName,
		Name:     check.Name,
		Network:  fields[0],
		Listener: fields[1],
		Domain:   fields[2],
		Query:    fields[3],
		Rcode:    rcode,
		Answers:  answers,
	}, true
}

func collectE2EAPIChecks(cases []e2eCaseReport) []e2eAPICheckView {
	items := make([]e2eAPICheckView, 0, 8)
	for _, item := range cases {
		for _, check := range item.Checks {
			method, path, ok := parseE2EAPICheck(check.Name)
			if !ok {
				continue
			}
			items = append(items, e2eAPICheckView{
				Case:   item.Name,
				Name:   check.Name,
				Method: method,
				Path:   path,
				Detail: check.Detail,
			})
		}
	}
	return items
}

func parseE2EAPICheck(name string) (string, string, bool) {
	fields := strings.Fields(strings.TrimSpace(name))
	if len(fields) < 2 {
		return "", "", false
	}
	switch fields[0] {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
		return fields[0], fields[1], true
	default:
		return "", "", false
	}
}

func buildE2EPerformanceView(cases []e2eCaseReport) e2ePerformanceView {
	view := e2ePerformanceView{}
	for _, item := range cases {
		for _, metric := range item.Metrics {
			row := e2eMetricView{
				Case:   item.Name,
				Name:   metric.Name,
				Value:  metric.Value,
				Detail: metric.Detail,
			}
			switch {
			case strings.HasPrefix(metric.Name, "load "):
				view.LoadMetrics = append(view.LoadMetrics, row)
			case strings.HasPrefix(metric.Name, "cache_"):
				view.CacheMetrics = append(view.CacheMetrics, row)
			default:
				view.OtherMetrics = append(view.OtherMetrics, row)
			}
		}
	}
	view.Highlights = selectE2EHighlights(view)
	return view
}

func selectE2EHighlights(view e2ePerformanceView) []e2eMetricView {
	order := []string{"load qps", "load p95 latency", "load avg latency", "load success"}
	highlights := make([]e2eMetricView, 0, len(order))
	for _, name := range order {
		if metric, ok := findE2EMetricByName(view.LoadMetrics, name); ok {
			highlights = append(highlights, metric)
		}
	}
	if len(highlights) > 0 {
		return highlights
	}
	limit := minE2EMetricCount(4, len(view.OtherMetrics))
	return append([]e2eMetricView(nil), view.OtherMetrics[:limit]...)
}

func findE2EMetricByName(items []e2eMetricView, name string) (e2eMetricView, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return e2eMetricView{}, false
}

func minE2EMetricCount(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func buildE2ECoverage(
	dnsChecks []e2eDNSCheckView,
	apiChecks []e2eAPICheckView,
	performance e2ePerformanceView,
) e2eCoverageView {
	return e2eCoverageView{
		DNSChecks:    len(dnsChecks),
		APIChecks:    len(apiChecks),
		LoadMetrics:  len(performance.LoadMetrics),
		CacheMetrics: len(performance.CacheMetrics),
	}
}
