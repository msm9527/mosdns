package cache

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/maphash"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe" // 性能补丁引入

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/cache"
	"github.com/IrineSistiana/mosdns/v5/pkg/dnsutils"
	"github.com/IrineSistiana/mosdns/v5/pkg/pool"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/pkg/utils"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
	"github.com/go-chi/chi/v5"
	"github.com/klauspost/compress/gzip"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"golang.org/x/exp/constraints"
	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"
)

const (
	PluginType = "cache"
)

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
	sequence.MustRegExecQuickSetup(PluginType, quickSetupCache)
}

const (
	defaultLazyUpdateTimeout = time.Second * 5
	expiredMsgTtl            = 5

	minimumChangesToDump   = 1024
	dumpHeader             = "mosdns_cache_v2"
	dumpBlockSize          = 128
	dumpMaximumBlockLength = 1 << 20 // 1M block. 8kb pre entry. Should be enough.

	shardCount = 256 // 256分段锁，平衡锁竞争与内存开销

	defaultL1TotalCap = 8192 // 默认 L1 总容量（按路由器场景收敛）
	defaultL1SmallCap = 4096 // 小容量实例默认 L1 总容量
	maxL1ShardCap     = 1024 // 防止误配导致单分片过大

	// size <= 600000 视为中小实例，自动使用更保守的 L1 档位。
	l1SmallCapThreshold = 600000

	// 性能补丁：后台更新并发上限，保护 CPU 不被瞬间过期的缓存任务占满
	maxConcurrentLazyUpdate = 256
)

const (
	adBit = 1 << iota
	cdBit
	doBit
)

var _ sequence.RecursiveExecutable = (*Cache)(nil)

// keyBufferPool 用于复用生成 Key 时的字节缓冲区，显著降低内存分配压力
var keyBufferPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 256)
		return &b
	},
}

// 性能补丁：复用 dns.Msg 对象用于 Unpack 解包过程
var dnsMsgPool = sync.Pool{
	New: func() any {
		return new(dns.Msg)
	},
}

// key defines the type used for cache keys.
type key string

var seed = maphash.MakeSeed()

func (k key) Sum() uint64 {
	return maphash.String(seed, string(k))
}

// item stores the cached response in L2.
type item struct {
	resp           []byte
	storedTime     time.Time
	expirationTime time.Time
	domainSet      string
}

// l1Item 存储解包后的对象，携带 domainSet，用于热点极速查询
type l1Item struct {
	msg            *dns.Msg
	storedTime     time.Time
	expirationTime time.Time
	domainSet      string
}

// l1Shard 带有 FIFO 限制的分段锁桶
type l1Shard struct {
	sync.RWMutex
	items   map[key]*l1Item
	order   []key
	pos     int
	ref     map[key]bool
	maxSize int
}

type Args struct {
	Size            int      `yaml:"size"`
	LazyCacheTTL    int      `yaml:"lazy_cache_ttl"`
	NXDomainTTL     int      `yaml:"nxdomain_ttl"`
	ServfailTTL     int      `yaml:"servfail_ttl"`
	L1Enabled       *bool    `yaml:"l1_enabled"`
	L1TotalCap      int      `yaml:"l1_total_cap"`
	L1ShardCap      int      `yaml:"l1_shard_cap"`
	EnableECS       bool     `yaml:"enable_ecs"`
	ExcludeIPs      []string `yaml:"exclude_ip"`
	DumpFile        string   `yaml:"dump_file"`
	DumpInterval    int      `yaml:"dump_interval"`
	WALFile         string   `yaml:"wal_file"`
	WALSyncInterval int      `yaml:"wal_sync_interval"`
}

type argsRaw struct {
	Size            int         `yaml:"size"`
	LazyCacheTTL    int         `yaml:"lazy_cache_ttl"`
	NXDomainTTL     int         `yaml:"nxdomain_ttl"`
	ServfailTTL     int         `yaml:"servfail_ttl"`
	L1Enabled       *bool       `yaml:"l1_enabled"`
	L1TotalCap      int         `yaml:"l1_total_cap"`
	L1ShardCap      int         `yaml:"l1_shard_cap"`
	EnableECS       bool        `yaml:"enable_ecs"`
	ExcludeIP       interface{} `yaml:"exclude_ip"`
	DumpFile        string      `yaml:"dump_file"`
	DumpInterval    int         `yaml:"dump_interval"`
	WALFile         string      `yaml:"wal_file"`
	WALSyncInterval int         `yaml:"wal_sync_interval"`
}

// UnmarshalYAML supports both scalar (space-separated) and sequence forms for exclude_ip.
func (a *Args) UnmarshalYAML(node *yaml.Node) error {
	var raw argsRaw
	if err := node.Decode(&raw); err != nil {
		return err
	}
	a.Size = raw.Size
	a.LazyCacheTTL = raw.LazyCacheTTL
	a.NXDomainTTL = raw.NXDomainTTL
	a.ServfailTTL = raw.ServfailTTL
	a.L1Enabled = raw.L1Enabled
	a.L1TotalCap = raw.L1TotalCap
	a.L1ShardCap = raw.L1ShardCap
	a.DumpFile = raw.DumpFile
	a.DumpInterval = raw.DumpInterval
	a.WALFile = raw.WALFile
	a.WALSyncInterval = raw.WALSyncInterval
	a.EnableECS = raw.EnableECS

	switch v := raw.ExcludeIP.(type) {
	case string:
		a.ExcludeIPs = strings.Fields(v)
	case []interface{}:
		for _, x := range v {
			if s, ok := x.(string); ok {
				a.ExcludeIPs = append(a.ExcludeIPs, s)
			} else {
				return fmt.Errorf("exclude_ip list contains non-string: %#v", x)
			}
		}
	case nil:
		// nothing
	default:
		return fmt.Errorf("exclude_ip must be string or list, got %T", v)
	}
	return nil
}

func (a *Args) init() {
	utils.SetDefaultUnsignNum(&a.Size, 1024)
	utils.SetDefaultUnsignNum(&a.DumpInterval, 600)
	utils.SetDefaultUnsignNum(&a.WALSyncInterval, 1)
	utils.SetDefaultUnsignNum(&a.NXDomainTTL, 60)
	utils.SetDefaultUnsignNum(&a.ServfailTTL, 15)
	if a.L1Enabled == nil {
		defaultEnabled := true
		a.L1Enabled = &defaultEnabled
	}
	if *a.L1Enabled {
		utils.SetDefaultUnsignNum(&a.L1TotalCap, inferDefaultL1TotalCap(a.Size))
	}
	if a.L1ShardCap < 0 {
		a.L1ShardCap = 0
	}
	if a.WALFile == "" {
		a.WALFile = inferWALFileFromDump(a.DumpFile)
	}
}

func inferDefaultL1TotalCap(size int) int {
	if size > 0 && size <= l1SmallCapThreshold {
		return defaultL1SmallCap
	}
	return defaultL1TotalCap
}

func inferWALFileFromDump(dumpFile string) string {
	if dumpFile == "" {
		return ""
	}
	ext := filepath.Ext(dumpFile)
	if ext == ".dump" {
		return strings.TrimSuffix(dumpFile, ext) + ".wal"
	}
	return dumpFile + ".wal"
}

type Cache struct {
	args         *Args
	logger       *zap.Logger
	metricsTag   string
	backend      *cache.Cache[key, *item]
	lazyUpdateSF singleflight.Group
	closeOnce    sync.Once
	closeNotify  chan struct{}
	updatedKey   atomic.Uint64
	persistence  *persistenceManager
	runtimeState *cacheRuntimeState

	queryCount             atomic.Uint64
	hitCount               atomic.Uint64
	lazyHitCount           atomic.Uint64
	l1HitCount             atomic.Uint64
	l2HitCount             atomic.Uint64
	lazyUpdateCount        atomic.Uint64
	lazyUpdateDroppedCount atomic.Uint64

	// 分段 L1 池
	shards [shardCount]*l1Shard
	// 运行期 L1 配置
	l1Enabled  bool
	l1ShardCap int

	// dumpMu protects the dump file writing process
	dumpMu sync.Mutex

	queryTotal              prometheus.Counter
	hitTotal                prometheus.Counter
	lazyHitTotal            prometheus.Counter
	l1HitTotalMetric        prometheus.Counter
	l2HitTotalMetric        prometheus.Counter
	lazyUpdateTotalMetric   prometheus.Counter
	lazyUpdateDroppedMetric prometheus.Counter
	dumpTotalCounter        prometheus.Counter
	dumpErrorCounter        prometheus.Counter
	loadTotalCounter        prometheus.Counter
	loadErrorCounter        prometheus.Counter
	walAppendCounter        prometheus.Counter
	walAppendErrorCounter   prometheus.Counter
	walReplayCounter        prometheus.Counter
	walReplayErrorCounter   prometheus.Counter
	size                    prometheus.GaugeFunc
	l1SizeMetric            prometheus.GaugeFunc
	dumpDuration            prometheus.Histogram
	loadDuration            prometheus.Histogram
	walReplayDuration       prometheus.Histogram

	excludeNets []*net.IPNet // parsed exclude_ip CIDRs

	// 性能补丁：后台更新并发限制信号量
	lazyUpdateLimit chan struct{}
}

type Opts struct {
	Logger     *zap.Logger
	MetricsTag string
}

func Init(bp *coremain.BP, args any) (any, error) {
	c := NewCache(args.(*Args), Opts{
		Logger:     bp.L(),
		MetricsTag: bp.Tag(),
	})

	if err := c.RegMetricsTo(prometheus.WrapRegistererWithPrefix(PluginType+"_", bp.M().GetMetricsReg())); err != nil {
		return nil, fmt.Errorf("failed to register metrics, %w", err)
	}
	bp.RegAPI(c.Api())
	return c, nil
}

func quickSetupCache(bq sequence.BQ, s string) (any, error) {
	size := 0
	if len(s) > 0 {
		i, err := strconv.Atoi(s)
		if err != nil {
			return nil, fmt.Errorf("invalid size, %w", err)
		}
		size = i
	}
	return NewCache(&Args{Size: size}, Opts{Logger: bq.L()}), nil
}

func NewCache(args *Args, opts Opts) *Cache {
	args.init()

	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}

	// parse exclude_ip CIDRs
	var excludeNets []*net.IPNet
	for _, cidr := range args.ExcludeIPs {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			logger.Warn("invalid exclude_ip, skip", zap.String("cidr", cidr), zap.Error(err))
			continue
		}

		logger.Debug("parsed exclude_ip network", zap.String("input", cidr), zap.String("network", ipnet.String()))
		excludeNets = append(excludeNets, ipnet)
	}

	backend := cache.New[key, *item](cache.Opts{Size: args.Size})
	lb := map[string]string{"tag": opts.MetricsTag}
	l1Enabled := args.L1Enabled == nil || *args.L1Enabled
	l1ShardCap := computeL1ShardCap(args, l1Enabled)
	p := &Cache{
		args:            args,
		logger:          logger,
		metricsTag:      opts.MetricsTag,
		backend:         backend,
		closeNotify:     make(chan struct{}),
		excludeNets:     excludeNets,
		runtimeState:    newCacheRuntimeState(args.DumpFile, args.WALFile),
		lazyUpdateLimit: make(chan struct{}, maxConcurrentLazyUpdate),
		l1Enabled:       l1Enabled,
		l1ShardCap:      l1ShardCap,
	}
	p.persistence = newPersistenceManager(args, logger)
	p.initMetrics(lb)

	// 初始化桶 (FIFO 淘汰版)
	capHint := l1ShardCap
	if capHint < 1 {
		capHint = 1
	}
	for i := 0; i < shardCount; i++ {
		p.shards[i] = &l1Shard{
			items:   make(map[key]*l1Item, capHint),
			order:   make([]key, capHint),
			ref:     make(map[key]bool, capHint),
			maxSize: l1ShardCap,
		}
	}

	if err := p.loadDump(); err != nil {
		p.logger.Error("failed to load cache dump", zap.Error(err))
	}
	p.startDumpLoop()

	return p
}

func computeL1ShardCap(args *Args, enabled bool) int {
	if !enabled {
		return 0
	}
	if args.L1ShardCap > 0 {
		if args.L1ShardCap > maxL1ShardCap {
			return maxL1ShardCap
		}
		return args.L1ShardCap
	}
	total := args.L1TotalCap
	if total <= 0 {
		total = defaultL1TotalCap
	}
	cap := (total + shardCount - 1) / shardCount
	if cap < 1 {
		cap = 1
	}
	if cap > maxL1ShardCap {
		cap = maxL1ShardCap
	}
	return cap
}

// updateL1 实现热路径环形淘汰
func (s *l1Shard) updateL1(k key, msg *dns.Msg, storedTime, expirationTime time.Time, domainSet string) {
	if s.maxSize <= 0 {
		return
	}
	s.Lock()
	defer s.Unlock()

	// 命中则更新并标记为最近使用
	if _, ok := s.items[k]; ok {
		s.items[k] = &l1Item{msg: msg.Copy(), storedTime: storedTime, expirationTime: expirationTime, domainSet: domainSet}
		s.ref[k] = true
		return
	}

	// CLOCK 淘汰循环
	for {
		if s.pos >= s.maxSize {
			s.pos = 0
		}
		oldKey := s.order[s.pos]
		if oldKey == "" {
			break
		}

		if s.ref[oldKey] {
			s.ref[oldKey] = false
			s.pos = (s.pos + 1) % s.maxSize
			continue
		}

		delete(s.items, oldKey)
		delete(s.ref, oldKey)
		break
	}

	s.items[k] = &l1Item{msg: msg.Copy(), storedTime: storedTime, expirationTime: expirationTime, domainSet: domainSet}
	s.order[s.pos] = k
	s.ref[k] = true
	s.pos = (s.pos + 1) % s.maxSize
}

func (c *Cache) containsExcluded(msg *dns.Msg) bool {
	if len(c.excludeNets) == 0 {
		return false
	}
	for _, rr := range msg.Answer {
		var ip net.IP
		switch rr := rr.(type) {
		case *dns.A:
			ip = rr.A
		case *dns.AAAA:
			ip = rr.AAAA
		default:
			continue
		}
		for _, net := range c.excludeNets {
			if net.Contains(ip) {
				c.logger.Debug("skip lazy cache: excluded IP", zap.String("cidr", net.String()), zap.String("ip", ip.String()))
				return true
			}
		}
	}
	return false
}

func (c *Cache) RegMetricsTo(r prometheus.Registerer) error {
	for _, collector := range [...]prometheus.Collector{
		c.queryTotal,
		c.hitTotal,
		c.lazyHitTotal,
		c.l1HitTotalMetric,
		c.l2HitTotalMetric,
		c.lazyUpdateTotalMetric,
		c.lazyUpdateDroppedMetric,
		c.dumpTotalCounter,
		c.dumpErrorCounter,
		c.loadTotalCounter,
		c.loadErrorCounter,
		c.walAppendCounter,
		c.walAppendErrorCounter,
		c.walReplayCounter,
		c.walReplayErrorCounter,
		c.dumpDuration,
		c.loadDuration,
		c.walReplayDuration,
		c.size,
		c.l1SizeMetric,
	} {
		if err := r.Register(collector); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cache) Exec(ctx context.Context, qCtx *query_context.Context, next sequence.ChainWalker) error {
	c.queryTotal.Inc()
	c.queryCount.Add(1)
	q := qCtx.Q()

	// 补丁：获取 Key 的字节切片和原始 Pool 指针
	msgKeyBuf, bufPtr := getMsgKeyBytes(q, qCtx, c.args.EnableECS)
	if msgKeyBuf == nil {
		return next.ExecNext(ctx, qCtx)
	}

	// 性能补丁：利用 unsafe 转换进行零拷贝 Lookup
	// 注意：此变量 kStr 仅在 Exec 生命周期内有效
	kStr := *(*string)(unsafe.Pointer(&msgKeyBuf))
	k := key(kStr)

	h := k.Sum()
	shard := c.shards[h%shardCount]

	// --- L1 极速路径查询 (免解包) ---
	var (
		v1  *l1Item
		ok1 bool
	)
	if c.l1Enabled {
		shard.RLock()
		v1, ok1 = shard.items[k]
		shard.RUnlock()
	}

	now := time.Now()
	if ok1 && now.Before(v1.expirationTime) {
		c.hitTotal.Inc()
		c.hitCount.Add(1)
		c.l1HitTotalMetric.Inc()
		c.l1HitCount.Add(1)

		// 仅 Copy 一次，避免 TTL 修改污染缓存
		r := v1.msg.Copy()
		dnsutils.SubtractTTL(r, uint32(now.Sub(v1.storedTime).Seconds()))
		r.Id = q.Id

		qCtx.SetResponse(r)

		if v1.domainSet != "" {
			qCtx.StoreValue(query_context.KeyDomainSet, v1.domainSet)
		}

		// 归还 Key 缓冲区
		keyBufferPool.Put(bufPtr)
		return nil
	}

	// 命中 L1 失败或过期，需要正式生成 string Key 用于后续 L2 存储或异步任务
	msgKey := string(msgKeyBuf)
	kReal := key(msgKey)

	// 归还 Key 缓冲区
	keyBufferPool.Put(bufPtr)

	// --- L2 路径查询 ---
	cachedResp, lazyHit, domainSet := getRespFromCache(msgKey, c.backend, c.args.LazyCacheTTL > 0, expiredMsgTtl)
	if lazyHit {
		c.lazyHitTotal.Inc()
		c.lazyHitCount.Add(1)
		c.doLazyUpdate(msgKey, qCtx, next)
	}
	if cachedResp != nil {
		c.hitTotal.Inc()
		c.hitCount.Add(1)
		c.l2HitTotalMetric.Inc()
		c.l2HitCount.Add(1)
		cachedResp.Id = q.Id
		qCtx.SetResponse(cachedResp)
		if domainSet != "" {
			qCtx.StoreValue(query_context.KeyDomainSet, domainSet)
		}

		// 命中 L2 且未过期：晋升到 L1
		if c.l1Enabled && !lazyHit {
			v2, _, _ := c.backend.Get(kReal)
			if v2 != nil {
				shard.updateL1(kReal, cachedResp, v2.storedTime, v2.expirationTime, v2.domainSet)
			}
		}
		return nil
	}

	err := next.ExecNext(ctx, qCtx)
	r := qCtx.R()

	if r != nil && !c.containsExcluded(r) {
		if c.saveRespToCache(msgKey, qCtx) {
			c.updatedKey.Add(1)

			// 同时更新 L1
			minTTL := dnsutils.GetMinimalTTL(r)
			var dset string
			if val, ok := qCtx.GetValue(query_context.KeyDomainSet); ok {
				if s, isString := val.(string); isString {
					dset = s
				}
			}
			if c.l1Enabled {
				shard.updateL1(kReal, r, now, now.Add(time.Duration(minTTL)*time.Second), dset)
			}
		}
	}

	return err
}

func (c *Cache) doLazyUpdate(msgKey string, qCtx *query_context.Context, next sequence.ChainWalker) {
	qCtxCopy := qCtx.Copy()
	lazyUpdateFunc := func() (any, error) {
		c.lazyUpdateTotalMetric.Inc()
		c.lazyUpdateCount.Add(1)

		// 性能补丁：并发信号量控制
		select {
		case c.lazyUpdateLimit <- struct{}{}:
			defer func() { <-c.lazyUpdateLimit }()
		default:
			c.lazyUpdateDroppedMetric.Inc()
			c.lazyUpdateDroppedCount.Add(1)
			return nil, nil // 负载过高，放弃此次异步更新，保护 CPU
		}

		defer c.lazyUpdateSF.Forget(msgKey)
		qCtx := qCtxCopy

		c.logger.Debug("start lazy cache update", qCtx.InfoField())
		ctx, cancel := context.WithTimeout(context.Background(), defaultLazyUpdateTimeout)
		defer cancel()

		err := next.ExecNext(ctx, qCtx)
		if err != nil {
			if err != sequence.ErrExit {
				c.logger.Warn("failed to update lazy cache", qCtx.InfoField(), zap.Error(err))
			}
		}

		r := qCtx.R()
		if r != nil && !c.containsExcluded(r) {
			if c.saveRespToCache(msgKey, qCtx) {
				c.updatedKey.Add(1)
				// 更新 L1
				if c.l1Enabled {
					k := key(msgKey)
					h := k.Sum()
					shard := c.shards[h%shardCount]
					minTTL := dnsutils.GetMinimalTTL(r)
					var dset string
					if val, ok := qCtx.GetValue(query_context.KeyDomainSet); ok {
						if s, isString := val.(string); isString {
							dset = s
						}
					}
					shard.updateL1(k, r, time.Now(), time.Now().Add(time.Duration(minTTL)*time.Second), dset)
				}
			}
		}
		c.logger.Debug("lazy cache updated", qCtx.InfoField())
		return nil, nil
	}
	c.lazyUpdateSF.DoChan(msgKey, lazyUpdateFunc)
}

func (c *Cache) Close() error {
	if err := c.dumpCache(); err != nil {
		c.logger.Error("failed to dump cache", zap.Error(err))
	}
	if err := c.persistence.close(); err != nil {
		c.logger.Error("failed to close cache persistence", zap.Error(err))
	}
	c.closeOnce.Do(func() {
		close(c.closeNotify)
	})
	return c.backend.Close()
}

func (c *Cache) PrepareForRestart() error {
	return c.Close()
}

func (c *Cache) loadDump() error {
	if c.persistence == nil {
		return nil
	}
	return c.persistence.restore(c)
}

func (c *Cache) startDumpLoop() {
	if len(c.args.DumpFile) == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Duration(c.args.DumpInterval) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				keyUpdated := c.updatedKey.Swap(0)
				if keyUpdated < minimumChangesToDump {
					c.updatedKey.Add(keyUpdated)
					continue
				}
				if err := c.dumpCache(); err != nil {
					c.logger.Error("dump cache", zap.Error(err))
				}
			case <-c.closeNotify:
				return
			}
		}
	}()
}

func (c *Cache) dumpCache() error {
	c.dumpMu.Lock()
	defer c.dumpMu.Unlock()

	if c.persistence == nil {
		return nil
	}
	start := time.Now()
	c.dumpTotalCounter.Inc()
	en, err := c.persistence.checkpoint(c)
	c.dumpDuration.Observe(time.Since(start).Seconds())
	c.runtimeState.recordDump(en, time.Since(start), err)
	if err != nil {
		c.dumpErrorCounter.Inc()
		return fmt.Errorf("failed to write dump, %w", err)
	}
	c.logger.Info("cache dumped", zap.Int("entries", en))
	return nil
}

func (c *Cache) SaveToDisk(_ context.Context) error {
	if len(c.args.DumpFile) == 0 {
		return errors.New("dump_file is not configured in config file")
	}
	c.logger.Info("saving cache to disk via direct action")
	return c.dumpCache()
}

func (c *Cache) FlushRuntime(_ context.Context) error {
	c.logger.Info("flushing cache via direct action")
	c.backend.Flush()
	c.resetL1()
	c.updatedKey.Store(0)
	go func() {
		if err := c.dumpCache(); err != nil {
			c.logger.Error("failed to dump cache after direct flush", zap.Error(err))
		}
	}()
	return nil
}

func (c *Cache) Api() *chi.Mux {
	r := chi.NewRouter()

	// 清空缓存 API：执行后打扫卫生
	r.Get("/flush", coremain.WithAsyncGC(func(w http.ResponseWriter, req *http.Request) {
		if err := c.FlushRuntime(req.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Cache flushed and a background dump has been triggered.\n"))
	}))

	r.Get("/dump", coremain.WithAsyncGC(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("content-type", "application/octet-stream")
		_, err := c.writeDump(w)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}))

	r.Get("/save", func(w http.ResponseWriter, req *http.Request) {
		if err := c.SaveToDisk(req.Context()); err != nil {
			c.logger.Error("failed to save cache via api", zap.Error(err))
			http.Error(w, fmt.Sprintf("failed to save cache: %v", err), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fmt.Sprintf("Cache successfully saved to %s\n", c.args.DumpFile)))
	})

	r.Post("/load_dump", coremain.WithAsyncGC(func(w http.ResponseWriter, req *http.Request) {
		if _, err := c.readDump(req.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := c.dumpCache(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	r.Get("/stats", func(w http.ResponseWriter, req *http.Request) {
		c.writeStats(w)
	})

	// 优化后的查询 API：支持分页、后端搜索和分级匹配
	r.Get("/show", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `inline; filename="cache.txt"`)

		query := strings.ToLower(req.URL.Query().Get("q"))
		limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
		offset, _ := strconv.Atoi(req.URL.Query().Get("offset"))

		if limit <= 0 {
			limit = 100
		}
		if offset < 0 {
			offset = 0
		}

		isIPLike := strings.Contains(query, ".") || strings.Contains(query, ":")

		now := time.Now()
		matchedCount := 0
		sentCount := 0
		stopIteration := errors.New("limit reached")

		reusableMsg := new(dns.Msg)

		err := c.backend.Range(func(k key, v *item, cacheExpirationTime time.Time) error {
			if cacheExpirationTime.Before(now) {
				return nil
			}

			keyStr := keyToString(k)
			found := false

			// 第一级匹配：检查 Key
			if query == "" || strings.Contains(strings.ToLower(keyStr), query) {
				found = true
			}

			// 第二级匹配：启发式深度检查回答中的 IP
			isDeepMatched := false
			if !found && isIPLike {
				if err := reusableMsg.Unpack(v.resp); err == nil {
					for _, rr := range reusableMsg.Answer {
						if strings.Contains(rr.String(), query) {
							found = true
							isDeepMatched = true
							break
						}
					}
				}
			}

			if found {
				matchedCount++
				if matchedCount <= offset {
					return nil
				}

				fmt.Fprintf(w, "----- Cache Entry -----\n")
				fmt.Fprintf(w, "Key:           %s\n", keyStr)
				if v.domainSet != "" {
					fmt.Fprintf(w, "DomainSet:     %s\n", v.domainSet)
				}
				fmt.Fprintf(w, "StoredTime:    %s\n", v.storedTime.Format(time.RFC3339))
				fmt.Fprintf(w, "MsgExpire:     %s\n", v.expirationTime.Format(time.RFC3339))
				fmt.Fprintf(w, "CacheExpire:   %s\n", cacheExpirationTime.Format(time.RFC3339))

				if !isDeepMatched {
					if err := reusableMsg.Unpack(v.resp); err != nil {
						fmt.Fprintf(w, "DNS Message:\n<failed to unpack>\n")
						goto endLoop
					}
				}
				fmt.Fprintf(w, "DNS Message:\n%s\n", dnsMsgToString(reusableMsg))

			endLoop:
				sentCount++
				if sentCount >= limit {
					return stopIteration
				}
			}
			return nil
		})

		if err != nil && err != stopIteration {
			c.logger.Error("failed to enumerate cache", zap.Error(err))
		}
	})

	return r
}

// keyToString converts internal []byte key to human readable format
func keyToString(k key) string {
	data := []byte(k)
	offset := 0
	var parts []string

	// 1. flags (1 byte)
	if len(data) < offset+1 {
		return fmt.Sprintf("invalid_key(len<1): %x", data)
	}
	flagsByte := data[offset]
	offset++
	var flags []string
	if flagsByte&adBit != 0 {
		flags = append(flags, "AD")
	}
	if flagsByte&cdBit != 0 {
		flags = append(flags, "CD")
	}
	if flagsByte&doBit != 0 {
		flags = append(flags, "DO")
	}

	// 2. QType (2 bytes)
	if len(data) < offset+2 {
		return fmt.Sprintf("invalid_key(len<3): %x", data)
	}
	qtype := binary.BigEndian.Uint16(data[offset : offset+2])
	offset += 2

	// 3. Name
	if len(data) < offset+1 {
		return fmt.Sprintf("invalid_key(len<4): %x", data)
	}
	nameLen := int(data[offset])
	offset++
	if len(data) < offset+nameLen {
		return fmt.Sprintf("invalid_key(incomplete_name): %x", data)
	}
	qname := string(data[offset : offset+nameLen])
	parts = append(parts, qname, dns.TypeToString[qtype], "IN")
	offset += nameLen

	if len(flags) > 0 {
		parts = append(parts, fmt.Sprintf("[flags:%s]", strings.Join(flags, ",")))
	}

	// 4. ECS (optional)
	if offset < len(data) {
		if len(data) < offset+1 {
			parts = append(parts, "[ecs:invalid_len_byte]")
		} else {
			ecsLen := int(data[offset])
			offset++
			if len(data) < offset+ecsLen {
				parts = append(parts, "[ecs:incomplete_string]")
			} else {
				ecs := string(data[offset : offset+ecsLen])
				parts = append(parts, fmt.Sprintf("[ecs:%s]", ecs))
			}
		}
	}

	return strings.Join(parts, " ")
}

func dnsMsgToString(msg *dns.Msg) string {
	if msg == nil {
		return "<nil>\n"
	}
	return strings.TrimSpace(msg.String()) + "\n"
}

func (c *Cache) writeDump(w io.Writer) (int, error) {
	en := 0
	gw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	gw.Name = dumpHeader

	block := new(CacheDumpBlock)
	writeBlock := func() error {
		b, err := proto.Marshal(block)
		if err != nil {
			return fmt.Errorf("failed to marshal protobuf, %w", err)
		}
		l := make([]byte, 8)
		binary.BigEndian.PutUint64(l, uint64(len(b)))
		if _, err := gw.Write(l); err != nil {
			return fmt.Errorf("failed to write header, %w", err)
		}
		if _, err := gw.Write(b); err != nil {
			return fmt.Errorf("failed to write data, %w", err)
		}
		en += len(block.GetEntries())
		block.Reset()
		return nil
	}

	now := time.Now()
	rangeFunc := func(k key, v *item, cacheExpirationTime time.Time) error {
		if cacheExpirationTime.Before(now) {
			return nil
		}
		e := &CachedEntry{
			Key:                 []byte(k),
			CacheExpirationTime: cacheExpirationTime.Unix(),
			MsgExpirationTime:   v.expirationTime.Unix(),
			MsgStoredTime:       v.storedTime.Unix(),
			Msg:                 v.resp,
			DomainSet:           v.domainSet,
		}
		block.Entries = append(block.Entries, e)
		if len(block.Entries) >= dumpBlockSize {
			return writeBlock()
		}
		return nil
	}
	if err := c.backend.Range(rangeFunc); err != nil {
		return en, err
	}
	if len(block.GetEntries()) > 0 {
		if err := writeBlock(); err != nil {
			return en, err
		}
	}
	return en, gw.Close()
}

func (c *Cache) readDump(r io.Reader) (int, error) {
	en := 0
	gr, err := gzip.NewReader(r)
	if err != nil {
		return en, fmt.Errorf("failed to read gzip header, %w", err)
	}
	if gr.Name != dumpHeader {
		return en, fmt.Errorf("invalid or old cache dump, header is %s, want %s", gr.Name, dumpHeader)
	}

	var errReadHeaderEOF = errors.New("")
	readBlock := func() error {
		h := pool.GetBuf(8)
		defer pool.ReleaseBuf(h)
		_, err := io.ReadFull(gr, *h)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errReadHeaderEOF
			}
			return fmt.Errorf("failed to read block header, %w", err)
		}
		u := binary.BigEndian.Uint64(*h)
		if u > dumpMaximumBlockLength {
			return fmt.Errorf("invalid header, block length is big, %d", u)
		}
		b := pool.GetBuf(int(u))
		defer pool.ReleaseBuf(b)
		_, err = io.ReadFull(gr, *b)
		if err != nil {
			return fmt.Errorf("failed to read block data, %w", err)
		}
		block := new(CacheDumpBlock)
		if err := proto.Unmarshal(*b, block); err != nil {
			return fmt.Errorf("failed to decode block data, %w", err)
		}

		en += len(block.GetEntries())
		for _, entry := range block.GetEntries() {
			cacheExpTime := time.Unix(entry.GetCacheExpirationTime(), 0)
			msgExpTime := time.Unix(entry.GetMsgExpirationTime(), 0)
			storedTime := time.Unix(entry.GetMsgStoredTime(), 0)

			i := &item{
				resp:           entry.GetMsg(),
				storedTime:     storedTime,
				expirationTime: msgExpTime,
				domainSet:      entry.GetDomainSet(),
			}
			c.backend.Store(key(entry.GetKey()), i, cacheExpTime)
		}
		return nil
	}

	for {
		err = readBlock()
		if err != nil {
			if err == errReadHeaderEOF {
				err = nil
			}
			break
		}
	}

	if err != nil {
		return en, err
	}
	return en, gr.Close()
}

func getECSClient(qCtx *query_context.Context) string {
	queryOpt := qCtx.QOpt()
	for _, o := range queryOpt.Option {
		if o.Option() == dns.EDNS0SUBNET {
			return o.String()
		}
	}
	return ""
}

// 补丁优化：返回字节切片以便执行零拷贝 Lookup
func getMsgKeyBytes(q *dns.Msg, qCtx *query_context.Context, useECS bool) ([]byte, *[]byte) {
	if q.Response || q.Opcode != dns.OpcodeQuery || len(q.Question) != 1 {
		return nil, nil
	}

	question := q.Question[0]
	totalLen := 1 + 2 + 1 + len(question.Name)
	ecs := ""
	if useECS {
		ecs = getECSClient(qCtx)
		totalLen += 1 + len(ecs)
	}

	bufPtr := keyBufferPool.Get().(*[]byte)
	buf := (*bufPtr)[:0]
	if cap(buf) < totalLen {
		buf = make([]byte, 0, totalLen+32)
	}

	b := byte(0)
	if q.AuthenticatedData {
		b = b | adBit
	}
	if q.CheckingDisabled {
		b = b | cdBit
	}
	if opt := q.IsEdns0(); opt != nil && opt.Do() {
		b = b | doBit
	}

	buf = append(buf, b)
	buf = append(buf, byte(question.Qtype>>8), byte(question.Qtype))
	buf = append(buf, byte(len(question.Name)))
	buf = append(buf, question.Name...)
	if len(ecs) > 0 {
		buf = append(buf, byte(len(ecs)))
		buf = append(buf, ecs...)
	}

	*bufPtr = buf
	return buf, bufPtr
}

func copyNoOpt(m *dns.Msg) *dns.Msg {
	if m == nil {
		return nil
	}

	m2 := new(dns.Msg)
	m2.MsgHdr = m.MsgHdr
	m2.Compress = m.Compress

	if len(m.Question) > 0 {
		m2.Question = make([]dns.Question, len(m.Question))
		copy(m2.Question, m.Question)
	}

	lenExtra := len(m.Extra)
	for _, r := range m.Extra {
		if r.Header().Rrtype == dns.TypeOPT {
			lenExtra--
		}
	}

	s := make([]dns.RR, len(m.Answer)+len(m.Ns)+lenExtra)
	m2.Answer, s = s[:0:len(m.Answer)], s[len(m.Answer):]
	m2.Ns, s = s[:0:len(m.Ns)], s[len(m.Ns):]
	m2.Extra = s[:0:lenExtra]

	for _, r := range m.Answer {
		m2.Answer = append(m2.Answer, dns.Copy(r))
	}
	for _, r := range m.Ns {
		m2.Ns = append(m2.Ns, dns.Copy(r))
	}

	for _, r := range m.Extra {
		if r.Header().Rrtype == dns.TypeOPT {
			continue
		}
		m2.Extra = append(m2.Extra, dns.Copy(r))
	}
	return m2
}

func min[T constraints.Ordered](a, b T) T {
	if a < b {
		return a
	}
	return b
}

func getRespFromCache(msgKey string, backend *cache.Cache[key, *item], lazyCacheEnabled bool, lazyTtl int) (*dns.Msg, bool, string) {
	v, _, _ := backend.Get(key(msgKey))
	if v != nil {
		now := time.Now()

		// 性能补丁：利用 Pool 进行解包，减少对象分配
		m := dnsMsgPool.Get().(*dns.Msg)
		defer dnsMsgPool.Put(m)

		if err := m.Unpack(v.resp); err != nil {
			backend.Delete(key(msgKey))
			return nil, false, ""
		}

		if now.Before(v.expirationTime) {
			// 这里必须 Copy，因为下游会修改 TTL 或 ID
			r := m.Copy()
			dnsutils.SubtractTTL(r, uint32(now.Sub(v.storedTime).Seconds()))
			return r, false, v.domainSet
		}

		if lazyCacheEnabled {
			r := m.Copy()
			dnsutils.SetTTL(r, uint32(lazyTtl))
			return r, true, v.domainSet
		}
	}
	return nil, false, ""
}

func (c *Cache) saveRespToCache(msgKey string, qCtx *query_context.Context) bool {
	r := qCtx.R()
	if r == nil || r.Truncated != false {
		return false
	}

	var msgTtl time.Duration
	var cacheTtl time.Duration
	switch r.Rcode {
	case dns.RcodeNameError:
		msgTtl = time.Duration(c.args.NXDomainTTL) * time.Second
		cacheTtl = msgTtl
	case dns.RcodeServerFailure:
		msgTtl = time.Duration(c.args.ServfailTTL) * time.Second
		cacheTtl = msgTtl
	case dns.RcodeSuccess:
		minTTL := dnsutils.GetMinimalTTL(r)
		if len(r.Answer) == 0 { // Empty answer. Set ttl between 0~300.
			const maxEmtpyAnswerTtl = 300
			msgTtl = time.Duration(min(minTTL, maxEmtpyAnswerTtl)) * time.Second
			if c.args.LazyCacheTTL > 0 {
				cacheTtl = time.Duration(c.args.LazyCacheTTL) * time.Second
			} else {
				cacheTtl = msgTtl
			}
		} else {
			msgTtl = time.Duration(minTTL) * time.Second
			if c.args.LazyCacheTTL > 0 {
				cacheTtl = time.Duration(c.args.LazyCacheTTL) * time.Second
			} else {
				cacheTtl = msgTtl
			}
		}
	}

	const minCacheableTTL = 5 * time.Second
	if msgTtl <= 0 {
		msgTtl = minCacheableTTL
	}
	if cacheTtl <= 0 {
		cacheTtl = minCacheableTTL
	}

	msgToCache := copyNoOpt(r)
	packedMsg, err := msgToCache.Pack()
	if err != nil {
		return false
	}

	now := time.Now()
	v := &item{
		resp:           packedMsg,
		storedTime:     now,
		expirationTime: now.Add(msgTtl),
	}

	if val, ok := qCtx.GetValue(query_context.KeyDomainSet); ok {
		if name, isString := val.(string); isString {
			v.domainSet = name
		}
	}

	cacheExp := now.Add(cacheTtl)
	c.backend.Store(key(msgKey), v, cacheExp)
	if err := c.persistence.appendStore(walStoreRecord{
		key:       key(msgKey),
		cacheExp:  cacheExp,
		cacheItem: v,
	}); err != nil {
		c.walAppendErrorCounter.Inc()
		c.logger.Warn("failed to append cache wal", zap.Error(err))
	} else if c.args.WALFile != "" {
		c.walAppendCounter.Inc()
	}
	return true
}
