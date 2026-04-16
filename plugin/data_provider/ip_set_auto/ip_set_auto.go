// Package ip_set_auto 是 ip_set 的增强版数据提供插件，通过 Linux 邻居表（ARP/NDP）
// 自动发现双栈设备并扩展匹配列表。
//
// 用户只需将配置中的 type: ip_set 改为 type: ip_set_auto，即可让黑白名单
// 同时覆盖同一设备的 IPv4 和 IPv6 地址，无需手动维护双栈映射。
//
// 工作原理：
//  1. 加载原始 IP 列表（与 ip_set 完全相同）
//  2. 对列表中的每个单主机地址（/32 或 /128），查询邻居表获取 MAC 地址
//  3. 通过同一 MAC 找到该设备的所有 IP（IPv4 + IPv6）
//  4. 将这些兄弟地址加入扩展匹配列表
//  5. 后台定时刷新邻居表，自动更新扩展列表
package ip_set_auto

import (
	"bufio"
	"context"
	"fmt"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/netlist"
	"github.com/IrineSistiana/mosdns/v5/pkg/neigh"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider"
	"github.com/IrineSistiana/mosdns/v5/plugin/data_provider/ip_set"
	"go.uber.org/zap"
)

const PluginType = "ip_set_auto"

func init() {
	coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(ip_set.Args) })
}

// IPSetAuto 在 ip_set 基础上添加邻居表扩展能力。
type IPSetAuto struct {
	bp         *coremain.BP
	pluginTag  string
	baseArgs   *ip_set.Args
	logger     *zap.Logger
	neighCache *neigh.Cache

	// matcherVal 存储当前生效的 netlist.Matcher（含扩展 IP）
	matcherVal atomic.Value

	// baseList 存储原始加载的 IP 列表（不含扩展）
	baseList *netlist.List
	files    []string

	// otherSets 存储引用的其他 ip_set 实例
	otherSets []netlist.Matcher

	mutex sync.Mutex
}

// 确保实现所有必要接口
var (
	_ data_provider.IPMatcherProvider = (*IPSetAuto)(nil)
	_ netlist.Matcher                 = (*IPSetAuto)(nil)
	_ coremain.ControlConfigReloader  = (*IPSetAuto)(nil)
	_ coremain.ListContentController  = (*IPSetAuto)(nil)
)

func (d *IPSetAuto) GetIPMatcher() netlist.Matcher {
	return d
}

func (d *IPSetAuto) Match(addr netip.Addr) bool {
	m, ok := d.matcherVal.Load().(netlist.Matcher)
	if !ok || m == nil {
		return false
	}
	return m.Match(addr)
}

// Init 是插件入口
func Init(bp *coremain.BP, args any) (any, error) {
	return NewIPSetAuto(bp, args.(*ip_set.Args))
}

// NewIPSetAuto 创建并初始化 IPSetAuto 实例
func NewIPSetAuto(bp *coremain.BP, args *ip_set.Args) (*IPSetAuto, error) {
	baseArgs := cloneArgs(args)
	if rawArgs, ok := bp.RawArgs().(*ip_set.Args); ok && rawArgs != nil {
		baseArgs = cloneArgs(rawArgs)
	}

	d := &IPSetAuto{
		bp:         bp,
		pluginTag:  bp.Tag(),
		baseArgs:   baseArgs,
		logger:     bp.L(),
		neighCache: neigh.DefaultCache(),
		baseList:   netlist.NewList(),
		files:      append([]string(nil), args.Files...),
	}

	// 加载原始 IP 列表
	if err := ip_set.LoadFromIPsAndFiles(args.IPs, args.Files, d.baseList); err != nil {
		return nil, err
	}
	d.baseList.Sort()

	// 加载引用的其他 sets
	for _, tag := range args.Sets {
		provider, _ := bp.Plugin(tag).(data_provider.IPMatcherProvider)
		if provider == nil {
			return nil, fmt.Errorf("%s is not an IPMatcherProvider", tag)
		}
		d.otherSets = append(d.otherSets, provider.GetIPMatcher())
	}

	// 首次构建扩展匹配器
	d.rebuildExpanded()

	// 订阅邻居表更新
	d.neighCache.Subscribe(func() {
		d.rebuildExpanded()
	})

	return d, nil
}

// rebuildExpanded 根据当前 baseList 和邻居表构建扩展匹配器。
func (d *IPSetAuto) rebuildExpanded() {
	d.mutex.Lock()
	base := d.baseList
	others := d.otherSets
	d.mutex.Unlock()

	if base == nil || base.Len() == 0 {
		d.storeSnapshot(base, others)
		return
	}

	// 提取主机地址并查找兄弟
	expanded := netlist.NewList()
	expandedCount := 0

	// 先复制所有原始条目到扩展列表
	base.ForEach(func(pfx netip.Prefix) {
		expanded.Append(pfx)
	})

	// 收集需要扩展的单主机地址
	seen := make(map[netip.Addr]bool)
	hosts := collectHostAddrs(base)
	for _, h := range hosts {
		seen[h] = true
	}

	// 对每个主机地址查找邻居表兄弟
	for _, host := range hosts {
		siblings := d.neighCache.SiblingIPs(host)
		for _, sib := range siblings {
			if !seen[sib] {
				seen[sib] = true
				expanded.Append(netip.PrefixFrom(sib, sib.BitLen()))
				expandedCount++
			}
		}
	}

	expanded.Sort()

	if expandedCount > 0 {
		d.logger.Info("neighbor expansion applied",
			zap.String("tag", d.pluginTag),
			zap.Int("base_entries", base.Len()),
			zap.Int("expanded_by", expandedCount),
			zap.Int("total", expanded.Len()),
		)
	}

	d.storeSnapshot(expanded, others)
}

// storeSnapshot 原子更新匹配器快照
func (d *IPSetAuto) storeSnapshot(list *netlist.List, otherSets []netlist.Matcher) {
	var mg ip_set.MatcherGroup
	if list != nil && list.Len() > 0 {
		mg = append(mg, list)
	}
	if len(otherSets) > 0 {
		mg = append(mg, otherSets...)
	}
	d.matcherVal.Store(netlist.Matcher(mg))
}

// collectHostAddrs 从已排序的列表中提取所有单主机地址（用于邻居扩展）。
// 列表内部存储 IPv4 为 IPv4-mapped-IPv6（/128），需要 Unmap 还原。
func collectHostAddrs(list *netlist.List) []netip.Addr {
	var hosts []netip.Addr
	list.ForEach(func(pfx netip.Prefix) {
		if pfx.Bits() != 128 {
			return // 子网前缀，跳过
		}
		addr := pfx.Addr()
		if addr.Is4In6() {
			addr = addr.Unmap()
		}
		hosts = append(hosts, addr)
	})
	return hosts
}

// ReloadControlConfig 实现热重载接口
func (d *IPSetAuto) ReloadControlConfig(global *coremain.GlobalOverrides, _ []coremain.UpstreamOverrideConfig) error {
	effective := new(ip_set.Args)
	if err := coremain.DecodeRawArgsWithGlobalOverrides(d.pluginTag, d.baseArgs, effective, global); err != nil {
		return err
	}

	list := netlist.NewList()
	if err := ip_set.LoadFromIPsAndFiles(effective.IPs, effective.Files, list); err != nil {
		return err
	}
	list.Sort()

	otherSets := make([]netlist.Matcher, 0, len(effective.Sets))
	for _, tag := range effective.Sets {
		provider, _ := d.bp.Plugin(tag).(data_provider.IPMatcherProvider)
		if provider == nil {
			return fmt.Errorf("%s is not an IPMatcherProvider", tag)
		}
		otherSets = append(otherSets, provider.GetIPMatcher())
	}

	d.mutex.Lock()
	d.baseList = list
	d.files = append([]string(nil), effective.Files...)
	d.otherSets = otherSets
	d.mutex.Unlock()

	// 触发重建（会读取新的 baseList）
	d.rebuildExpanded()
	return nil
}

// ListEntries 返回原始（未扩展）的 IP 列表，供 API 查看。
func (d *IPSetAuto) ListEntries(query string, offset, limit int) ([]coremain.ListEntry, int, error) {
	d.mutex.Lock()
	l := d.baseList
	d.mutex.Unlock()

	items := make([]coremain.ListEntry, 0)
	if l != nil {
		l.ForEach(func(pfx netip.Prefix) {
			items = append(items, coremain.ListEntry{Value: normalizePrefix(pfx).String()})
		})
	}

	total := len(items)
	if offset < 0 {
		offset = 0
	}
	if limit > 0 && offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		items = items[offset:end]
	} else if offset > 0 && offset < total {
		items = items[offset:]
	} else if offset >= total {
		items = []coremain.ListEntry{}
	}
	return items, total, nil
}

// ReplaceListRuntime 替换运行时列表内容，自动触发邻居扩展重建。
func (d *IPSetAuto) ReplaceListRuntime(_ context.Context, values []string) (int, error) {
	tmpList := netlist.NewList()
	for _, s := range values {
		pfx, err := parseNetipPrefix(s)
		if err != nil {
			continue
		}
		tmpList.Append(pfx)
	}
	tmpList.Sort()

	d.mutex.Lock()
	d.baseList = tmpList
	d.mutex.Unlock()

	// 保存到文件
	if err := d.saveToFiles(); err != nil {
		return 0, err
	}

	// 触发邻居扩展重建
	d.rebuildExpanded()
	return tmpList.Len(), nil
}

// saveToFiles 将当前 baseList 保存到配置的文件
func (d *IPSetAuto) saveToFiles() error {
	d.mutex.Lock()
	files := d.files
	list := d.baseList
	d.mutex.Unlock()

	for _, path := range files {
		if err := writeListToFile(path, list); err != nil {
			return err
		}
	}
	return nil
}

func writeListToFile(path string, list *netlist.List) error {
	if path == "" || list == nil {
		return nil
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	var writeErr error
	list.ForEach(func(pfx netip.Prefix) {
		if writeErr == nil {
			_, writeErr = w.WriteString(normalizePrefix(pfx).String() + "\n")
		}
	})
	if writeErr != nil {
		f.Close()
		return writeErr
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// normalizePrefix 将内部存储的 IPv4-mapped-IPv6 还原为 IPv4 显示格式
func normalizePrefix(p netip.Prefix) netip.Prefix {
	addr := p.Addr()
	if addr.Is4In6() {
		unmapped := addr.Unmap()
		bits := p.Bits() - 96
		if bits < 0 {
			bits = 0
		}
		pfx, _ := unmapped.Prefix(bits)
		return pfx
	}
	return p
}

func parseNetipPrefix(s string) (netip.Prefix, error) {
	if idx := len(s); idx > 0 {
		for i := 0; i < len(s); i++ {
			if s[i] == '/' {
				return netip.ParsePrefix(s)
			}
		}
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

func cloneArgs(src *ip_set.Args) *ip_set.Args {
	if src == nil {
		return new(ip_set.Args)
	}
	return &ip_set.Args{
		IPs:   append([]string(nil), src.IPs...),
		Sets:  append([]string(nil), src.Sets...),
		Files: append([]string(nil), src.Files...),
	}
}
