package tools

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/IrineSistiana/mosdns/v5/mlog"
	"github.com/miekg/dns"
	"github.com/spf13/cobra"
	"golang.org/x/time/rate"
)

const (
	defaultStressTotalQueries  = 100000
	defaultStressUniqueDomains = 10000
)

var defaultStressSourceURLs = []string{
	"https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/refs/heads/meta/geo-lite/geosite/cn.list",
	"https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/refs/heads/meta/geo-lite/geosite/google.list",
	"https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/refs/heads/meta/geo-lite/geosite/netflix.list",
	"https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/refs/heads/meta/geo-lite/geosite/proxy.list",
}

type dnsStressOptions struct {
	Server         string
	DomainsFile    string
	SourceURLs     []string
	ReportFile     string
	FailuresFile   string
	TotalQueries   int
	TargetUnique   int
	Concurrency    int
	Timeout        time.Duration
	QPS            int
	TCPQPS         int
	TCPSample      int
	HotsetRatio    float64
	AllowGenerated bool
	QType          uint16
	QTypeName      string
}

type stressQuestion struct {
	Name  string `json:"name"`
	QType uint16 `json:"qtype"`
}

type phaseAccumulator struct {
	mu          sync.Mutex
	latencies   []float64
	rcodeCounts map[string]int
	failures    []failureSample
	responses   int
	timeouts    int
	errors      int
}

type phaseQueryResult struct {
	latencyMs float64
	rcode     string
	err       error
	timeout   bool
}

func newStressCmd() *cobra.Command {
	stressCmd := &cobra.Command{
		Use:   "stress",
		Short: "Run load and QoS tests against mosdns.",
	}
	stressCmd.AddCommand(newStressDNSCmd())
	return stressCmd
}

func newStressDNSCmd() *cobra.Command {
	opts := dnsStressOptions{
		TotalQueries: defaultStressTotalQueries,
		TargetUnique: defaultStressUniqueDomains,
		Concurrency:  200,
		Timeout:      2 * time.Second,
		QType:        dns.TypeA,
		QTypeName:    dns.TypeToString[dns.TypeA],
		SourceURLs:   append([]string(nil), defaultStressSourceURLs...),
		TCPSample:    0,
		HotsetRatio:  0.2,
		ReportFile:   "stress-report.json",
		FailuresFile: "stress-failures.ndjson",
	}

	cmd := &cobra.Command{
		Use:          "dns",
		Short:        "Run a high-volume DNS load test.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDNSStress(cmd.Context(), &opts)
		},
	}

	flags := cmd.Flags()
	flags.StringVar(&opts.Server, "server", "127.0.0.1:53", "DNS server address")
	flags.StringVar(&opts.DomainsFile, "domains-file", "", "optional domain source file")
	flags.StringSliceVar(&opts.SourceURLs, "source-url", opts.SourceURLs, "remote domain source URLs, geosite-style list format is supported")
	flags.IntVar(&opts.TotalQueries, "count", opts.TotalQueries, "total number of DNS queries to send")
	flags.IntVar(&opts.TargetUnique, "unique-count", opts.TargetUnique, "target number of unique real domains for the cold-start phase")
	flags.IntVar(&opts.Concurrency, "concurrency", opts.Concurrency, "number of concurrent workers")
	flags.DurationVar(&opts.Timeout, "timeout", opts.Timeout, "per-query timeout")
	flags.IntVar(&opts.QPS, "qps", 0, "UDP send rate limit, 0 means unlimited")
	flags.IntVar(&opts.TCPQPS, "tcp-qps", 200, "TCP comparison phase rate limit, 0 means unlimited")
	flags.IntVar(&opts.TCPSample, "tcp-sample", opts.TCPSample, "number of domains sampled for TCP comparison")
	flags.Float64Var(&opts.HotsetRatio, "hotset-ratio", opts.HotsetRatio, "ratio of hot domains used for repeated cache-hit traffic, between 0 and 1")
	flags.BoolVar(&opts.AllowGenerated, "allow-generated", false, "allow synthetic domains when real domains are insufficient")
	flags.StringVar(&opts.ReportFile, "report", opts.ReportFile, "JSON report output path")
	flags.StringVar(&opts.FailuresFile, "failures", opts.FailuresFile, "NDJSON failure sample output path")
	flags.String("qtype", opts.QTypeName, "DNS query type: A or AAAA")

	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		qtype, err := parseQType(cmd.Flags().Lookup("qtype").Value.String())
		if err != nil {
			return err
		}
		opts.QType = qtype
		opts.QTypeName = dns.TypeToString[qtype]
		if opts.TotalQueries < 1 {
			return fmt.Errorf("count must be >= 1")
		}
		if opts.TargetUnique < 1 {
			return fmt.Errorf("unique-count must be >= 1")
		}
		if opts.TargetUnique > opts.TotalQueries {
			return fmt.Errorf("unique-count must be <= count")
		}
		if opts.Concurrency < 1 {
			return fmt.Errorf("concurrency must be >= 1")
		}
		if opts.TCPSample < 0 {
			return fmt.Errorf("tcp-sample must be >= 0")
		}
		if opts.HotsetRatio <= 0 || opts.HotsetRatio > 1 {
			return fmt.Errorf("hotset-ratio must be within (0,1]")
		}
		return nil
	}
	return cmd
}

func runDNSStress(ctx context.Context, opts *dnsStressOptions) error {
	httpClient := &http.Client{Timeout: 20 * time.Second}
	domains, preview, sourceInfo, err := buildDomainCorpus(ctx, httpClient, opts.DomainsFile, opts.SourceURLs, opts.TargetUnique, opts.AllowGenerated)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return fmt.Errorf("no real domains available from configured sources")
	}

	plan := buildTrafficPlan(domains, opts.TotalQueries, opts.QType, opts.HotsetRatio)

	report := dnsStressReport{
		GeneratedAt:           time.Now().UTC().Format(time.RFC3339),
		TargetServer:          opts.Server,
		QuestionType:          opts.QTypeName,
		RequestedTotalQueries: opts.TotalQueries,
		RequestedUnique:       opts.TargetUnique,
		UniqueDomains:         len(domains),
		RepeatedQueries:       len(plan.RepeatQuestions),
		HotsetSize:            plan.HotsetSize,
		Concurrency:           opts.Concurrency,
		QPS:                   opts.QPS,
		TCPSample:             minInt(opts.TCPSample, len(plan.ColdQuestions)),
		Source:                sourceInfo,
		GeneratedDomains:      preview,
	}

	coldPhase, coldFailures := runStressPhase(ctx, "udp-cold", "udp", opts.Server, plan.ColdQuestions, opts.QTypeName, opts.Concurrency, opts.QPS, opts.Timeout)
	report.Phases = append(report.Phases, coldPhase)
	allFailures := append([]failureSample(nil), coldFailures...)

	if len(plan.RepeatQuestions) > 0 {
		replayPhase, replayFailures := runStressPhase(ctx, "udp-cache-hit", "udp", opts.Server, plan.RepeatQuestions, opts.QTypeName, opts.Concurrency, opts.QPS, opts.Timeout)
		report.Phases = append(report.Phases, replayPhase)
		allFailures = append(allFailures, replayFailures...)
		report.CacheEffect = buildCacheEffectSummary(coldPhase, replayPhase)
	}

	if opts.TCPSample > 0 {
		tcpQuestions := plan.ColdQuestions[:minInt(opts.TCPSample, len(plan.ColdQuestions))]
		tcpWorkers := minInt(opts.Concurrency, maxInt(1, len(tcpQuestions)))
		tcpPhase, tcpFailures := runStressPhase(ctx, "tcp-compare", "tcp", opts.Server, tcpQuestions, opts.QTypeName, tcpWorkers, opts.TCPQPS, opts.Timeout)
		report.Phases = append(report.Phases, tcpPhase)
		allFailures = append(allFailures, tcpFailures...)
	}

	if err := writeJSONFile(opts.ReportFile, report); err != nil {
		return err
	}
	if err := writeFailureSamples(opts.FailuresFile, allFailures); err != nil {
		return err
	}

	mlog.S().Infow("stress test completed",
		"report", opts.ReportFile,
		"failures", opts.FailuresFile,
		"unique_domains", len(domains),
		"total_queries", opts.TotalQueries,
		"repeat_queries", len(plan.RepeatQuestions),
		"phases", len(report.Phases),
	)
	return nil
}

func runStressPhase(ctx context.Context, phaseName string, network string, server string, questions []stressQuestion, qtypeName string, concurrency int, qps int, timeout time.Duration) (phaseResult, []failureSample) {
	start := time.Now()
	acc := &phaseAccumulator{
		latencies:   make([]float64, 0, len(questions)),
		rcodeCounts: make(map[string]int),
		failures:    make([]failureSample, 0, minInt(200, len(questions))),
	}
	jobs := make(chan stressQuestion)
	var wg sync.WaitGroup
	var limiter *rate.Limiter
	if qps > 0 {
		limiter = rate.NewLimiter(rate.Limit(qps), maxInt(1, concurrency))
	}

	for i := 0; i < minInt(concurrency, maxInt(1, len(questions))); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &dns.Client{Net: network, Timeout: timeout}
			for q := range jobs {
				if limiter != nil {
					if err := limiter.Wait(ctx); err != nil {
						return
					}
				}
				acc.record(phaseName, network, qtypeName, q.Name, exchangeDNS(client, server, q))
			}
		}()
	}

	for _, q := range questions {
		jobs <- q
	}
	close(jobs)
	wg.Wait()

	seconds := time.Since(start).Seconds()
	result := phaseResult{
		Phase:         phaseName,
		Network:       network,
		QType:         qtypeName,
		Total:         len(questions),
		Responses:     acc.responses,
		ResponseRate:  safeRate(acc.responses, len(questions)),
		NoError:       acc.rcodeCounts[dns.RcodeToString[dns.RcodeSuccess]],
		NXDomain:      acc.rcodeCounts[dns.RcodeToString[dns.RcodeNameError]],
		ServFail:      acc.rcodeCounts[dns.RcodeToString[dns.RcodeServerFailure]],
		Timeouts:      acc.timeouts,
		Errors:        acc.errors,
		OtherRCodes:   buildOtherRCodes(acc.rcodeCounts),
		Duration:      time.Since(start).String(),
		DurationMs:    roundFloat(seconds*1000, 3),
		SentQPS:       safePerSecond(len(questions), seconds),
		ResponseQPS:   safePerSecond(acc.responses, seconds),
		Latency:       summarizeLatencies(acc.latencies),
		Breakdown:     buildPhaseBreakdown(len(questions), acc.rcodeCounts, acc.timeouts, acc.errors),
		FailureSample: append([]failureSample(nil), acc.failures...),
	}
	return result, append([]failureSample(nil), acc.failures...)
}

func exchangeDNS(client *dns.Client, server string, q stressQuestion) phaseQueryResult {
	msg := new(dns.Msg)
	msg.SetQuestion(q.Name, q.QType)
	start := time.Now()
	resp, _, err := client.Exchange(msg, server)
	latency := float64(time.Since(start).Microseconds()) / 1000
	if err != nil {
		return phaseQueryResult{latencyMs: latency, err: err, timeout: isTimeoutErr(err)}
	}
	rcode := dns.RcodeToString[resp.Rcode]
	if rcode == "" {
		rcode = fmt.Sprintf("rcode_%d", resp.Rcode)
	}
	return phaseQueryResult{latencyMs: latency, rcode: rcode}
}

func (a *phaseAccumulator) record(phaseName string, network string, qtype string, domain string, result phaseQueryResult) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.latencies = append(a.latencies, result.latencyMs)
	if result.err != nil {
		a.errors++
		if result.timeout {
			a.timeouts++
		}
		a.pushFailure(failureSample{Phase: phaseName, Network: network, Domain: domain, QType: qtype, Error: result.err.Error(), LatencyMs: roundFloat(result.latencyMs, 3)})
		return
	}
	a.responses++
	a.rcodeCounts[result.rcode]++
	if result.rcode != dns.RcodeToString[dns.RcodeSuccess] {
		a.pushFailure(failureSample{Phase: phaseName, Network: network, Domain: domain, QType: qtype, RCode: result.rcode, LatencyMs: roundFloat(result.latencyMs, 3)})
	}
}

func (a *phaseAccumulator) pushFailure(sample failureSample) {
	if len(a.failures) < 200 {
		a.failures = append(a.failures, sample)
	}
}
