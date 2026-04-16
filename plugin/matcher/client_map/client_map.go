/*
 * Copyright (C) 2020-2022, IrineSistiana
 *
 * This file is part of mosdns.
 *
 * mosdns is free software: you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * mosdns is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

// client_map 实现客户端分组匹配器，支持两种模式：
//
// # 静态模式（Tag 模式）
//
// 通过映射文件把同一设备的 IPv4 和 IPv6 地址归为一个"客户端组"，
// 使黑白名单对双栈客户端同时生效。
//
// 映射文件格式（每行一个分组，同一 tag 可出现多行）：
//
//	# 注释行
//	<tag> <ip1> [ip2] [cidr3] ...
//
// 示例：
//
//	my_pc 192.168.1.100 fd00::abcd:1234
//	kid   192.168.1.200 fd00::200
//	iot   192.168.1.0/28 fd00::100/120
//
// Quick setup:
//
//	matches: client_map &/path/to/map.txt tag1 [tag2] ...
//
// # 自动模式（Auto 模式）
//
// 通过读取系统邻居表（ARP/NDP），自动根据 MAC 地址发现同一设备的
// 所有 IP 地址。只需指定设备的任一 IP（"种子 IP"），同一 MAC 地址
// 下的所有 IPv4/IPv6 地址都会自动被匹配。
//
// Quick setup:
//
//	matches: client_map auto 192.168.1.100 [192.168.1.200] ...
//	matches: client_map auto &/path/to/seed_ips.txt [192.168.1.100] ...
//
// 种子 IP 文件格式（每行一个 IP，支持 # 注释）：
//
//	192.168.1.100
//	192.168.1.200
//
// 自动模式每 30 秒刷新邻居表（仅支持 Linux）。
package client_map

import (
	"bufio"
	"context"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/netlist"
	"github.com/IrineSistiana/mosdns/v5/pkg/neigh"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
)

const defaultAutoRefreshInterval = 30 * time.Second

const PluginType = "client_map"

func init() {
	sequence.MustRegMatchQuickSetup(PluginType, QuickSetup)
}

// neighReader 是读取邻居表的函数，方便测试时替换。
var neighReader = neigh.ReadAll

// QuickSetup 解析 quick setup 参数并构建匹配器。
//
// 静态模式: "&<map_file> [&<map_file2>] <tag1> [tag2] ..."
// 自动模式: "auto <ip1> [ip2] [&seed_file] ..."
func QuickSetup(_ sequence.BQ, s string) (sequence.Matcher, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("client_map: 参数不能为空")
	}

	// 检测自动模式
	if strings.EqualFold(fields[0], "auto") {
		return newAutoMatcher(fields[1:])
	}

	return newStaticMatcher(fields)
}

// newStaticMatcher 构建静态（tag）模式匹配器。
func newStaticMatcher(fields []string) (sequence.Matcher, error) {
	args := parseArgs(fields)
	if len(args.files) == 0 {
		return nil, fmt.Errorf("client_map: 至少需要一个映射文件（使用 & 前缀）")
	}
	if len(args.tags) == 0 {
		return nil, fmt.Errorf("client_map: 至少需要一个客户端组标签")
	}

	tagMap, err := loadMapFiles(args.files)
	if err != nil {
		return nil, fmt.Errorf("client_map: 加载映射文件失败: %w", err)
	}

	list := netlist.NewList()
	for _, tag := range args.tags {
		prefixes, ok := tagMap[tag]
		if !ok {
			return nil, fmt.Errorf("client_map: 标签 %q 在映射文件中未找到", tag)
		}
		list.Append(prefixes...)
	}
	list.Sort()

	return &clientMapMatcher{list: list}, nil
}

// newAutoMatcher 构建自动（邻居表）模式匹配器。
func newAutoMatcher(fields []string) (sequence.Matcher, error) {
	seedIPs, err := parseSeedArgs(fields)
	if err != nil {
		return nil, fmt.Errorf("client_map auto: %w", err)
	}
	if len(seedIPs) == 0 {
		return nil, fmt.Errorf("client_map auto: 至少需要一个种子 IP")
	}

	m := &autoClientMapMatcher{
		seedIPs:         seedIPs,
		refreshInterval: defaultAutoRefreshInterval,
		readNeigh:       neighReader,
	}
	// 立即做一次刷新，保证匹配器可用
	m.refresh()
	return m, nil
}

// parseSeedArgs 从参数列表中解析种子 IP。
// 支持裸 IP 和 &文件路径（文件中每行一个 IP）。
func parseSeedArgs(fields []string) ([]netip.Addr, error) {
	var seeds []netip.Addr
	for _, field := range fields {
		if strings.HasPrefix(field, "&") {
			path := coremain.ResolveMainConfigPath(strings.TrimPrefix(field, "&"))
			fileSeeds, err := loadSeedFile(path)
			if err != nil {
				return nil, fmt.Errorf("加载种子文件 %s: %w", field, err)
			}
			seeds = append(seeds, fileSeeds...)
		} else {
			addr, err := netip.ParseAddr(field)
			if err != nil {
				return nil, fmt.Errorf("无效的种子 IP %q: %w", field, err)
			}
			seeds = append(seeds, addr)
		}
	}
	return seeds, nil
}

// loadSeedFile 从文件加载种子 IP 列表。
func loadSeedFile(path string) ([]netip.Addr, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var addrs []netip.Addr
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		addr, err := netip.ParseAddr(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: 无效的 IP %q: %w", lineNum, line, err)
		}
		addrs = append(addrs, addr)
	}
	return addrs, scanner.Err()
}

type quickSetupArgs struct {
	files []string
	tags  []string
}

func parseArgs(fields []string) *quickSetupArgs {
	args := &quickSetupArgs{}
	for _, field := range fields {
		if strings.HasPrefix(field, "&") {
			args.files = append(args.files, strings.TrimPrefix(field, "&"))
		} else {
			args.tags = append(args.tags, field)
		}
	}
	return args
}

// loadMapFiles 加载多个映射文件，返回 tag → []netip.Prefix 索引。
func loadMapFiles(files []string) (map[string][]netip.Prefix, error) {
	tagMap := make(map[string][]netip.Prefix)
	for _, path := range files {
		resolved := coremain.ResolveMainConfigPath(path)
		if err := loadMapFile(resolved, tagMap); err != nil {
			return nil, fmt.Errorf("file %s: %w", path, err)
		}
	}
	return tagMap, nil
}

// loadMapFile 解析单个映射文件。
// 每行格式: <tag> <ip1> [ip2] [cidr3] ...
// 同一 tag 可出现在多行，地址会合并。
func loadMapFile(path string, tagMap map[string][]netip.Prefix) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			return fmt.Errorf("line %d: 至少需要一个标签和一个 IP/CIDR", lineNum)
		}

		tag := fields[0]
		for _, ipStr := range fields[1:] {
			pfx, err := parsePrefix(ipStr)
			if err != nil {
				return fmt.Errorf("line %d: 无效的 IP/CIDR %q: %w", lineNum, ipStr, err)
			}
			tagMap[tag] = append(tagMap[tag], pfx)
		}
	}
	return scanner.Err()
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.ContainsRune(s, '/') {
		return netip.ParsePrefix(s)
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// clientMapMatcher 实现 sequence.Matcher 和 fastMatcher 接口（静态模式）。
type clientMapMatcher struct {
	list *netlist.List
}

func (m *clientMapMatcher) Match(_ context.Context, qCtx *query_context.Context) (bool, error) {
	addr := qCtx.ServerMeta.ClientAddr
	if !addr.IsValid() {
		return false, nil
	}
	return m.list.Match(addr), nil
}

// GetFastCheck 提供快速匹配路径，避免 context 分配开销。
func (m *clientMapMatcher) GetFastCheck() func(qCtx *query_context.Context) bool {
	return func(qCtx *query_context.Context) bool {
		addr := qCtx.ServerMeta.ClientAddr
		if !addr.IsValid() {
			return false
		}
		return m.list.Match(addr)
	}
}

// autoClientMapMatcher 通过系统邻居表自动发现同一设备的所有 IP。
// 给定一组"种子 IP"，找到它们对应的 MAC 地址，然后匹配同一 MAC 下的
// 所有 IP 地址。使用 RWMutex + 定时刷新保证并发安全。
type autoClientMapMatcher struct {
	seedIPs         []netip.Addr
	refreshInterval time.Duration
	readNeigh       func() ([]neigh.Entry, error)

	mu          sync.RWMutex
	list        *netlist.List
	lastRefresh time.Time
}

func (m *autoClientMapMatcher) Match(_ context.Context, qCtx *query_context.Context) (bool, error) {
	addr := qCtx.ServerMeta.ClientAddr
	if !addr.IsValid() {
		return false, nil
	}
	m.maybeRefresh()
	m.mu.RLock()
	list := m.list
	m.mu.RUnlock()
	if list == nil {
		return false, nil
	}
	return list.Match(addr), nil
}

// maybeRefresh 在刷新间隔过期后重新读取邻居表并更新匹配列表。
func (m *autoClientMapMatcher) maybeRefresh() {
	m.mu.RLock()
	needRefresh := time.Since(m.lastRefresh) >= m.refreshInterval
	m.mu.RUnlock()
	if !needRefresh {
		return
	}
	m.refresh()
}

// refresh 读取邻居表，根据种子 IP 扩展出同 MAC 下的所有兄弟 IP。
func (m *autoClientMapMatcher) refresh() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// double check：避免多个 goroutine 同时刷新
	if time.Since(m.lastRefresh) < m.refreshInterval && m.list != nil {
		return
	}

	entries, err := m.readNeigh()
	if err != nil {
		// 邻居表读取失败时，如果已有旧列表则保持不变，
		// 否则用种子 IP 构建最小列表
		if m.list == nil {
			m.list = m.buildSeedOnlyList()
		}
		m.lastRefresh = time.Now()
		return
	}

	m.list = m.expandFromNeighbors(entries)
	m.lastRefresh = time.Now()
}

// expandFromNeighbors 根据邻居表条目，找到种子 IP 对应的 MAC，
// 然后收集同 MAC 下的所有 IP。
func (m *autoClientMapMatcher) expandFromNeighbors(entries []neigh.Entry) *netlist.List {
	// 构建 IP → MAC 和 MAC → []IP 索引
	ip2mac := make(map[netip.Addr]string, len(entries))
	mac2ips := make(map[string][]netip.Addr)
	for _, e := range entries {
		ip2mac[e.IP] = e.MAC
		mac2ips[e.MAC] = append(mac2ips[e.MAC], e.IP)
	}

	// 收集种子 IP 对应的 MAC 集合
	seedMACs := make(map[string]struct{})
	for _, seed := range m.seedIPs {
		if mac, ok := ip2mac[seed]; ok {
			seedMACs[mac] = struct{}{}
		}
	}

	// 收集所有匹配 MAC 下的 IP
	matchedIPs := make(map[netip.Addr]struct{})
	for mac := range seedMACs {
		for _, ip := range mac2ips[mac] {
			matchedIPs[ip] = struct{}{}
		}
	}

	// 种子 IP 本身也要包含（可能不在邻居表中）
	for _, seed := range m.seedIPs {
		matchedIPs[seed] = struct{}{}
	}

	list := netlist.NewList()
	for ip := range matchedIPs {
		pfx := netip.PrefixFrom(ip, ip.BitLen())
		list.Append(pfx)
	}
	list.Sort()
	return list
}

// buildSeedOnlyList 仅用种子 IP 构建匹配列表（降级模式）。
func (m *autoClientMapMatcher) buildSeedOnlyList() *netlist.List {
	list := netlist.NewList()
	for _, seed := range m.seedIPs {
		pfx := netip.PrefixFrom(seed, seed.BitLen())
		list.Append(pfx)
	}
	list.Sort()
	return list
}
