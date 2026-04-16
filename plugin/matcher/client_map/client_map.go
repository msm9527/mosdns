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

// client_map 实现客户端分组匹配器。
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
// Quick setup 格式：
//
//	matches: client_map &/path/to/map.txt tag1 [tag2] ...
package client_map

import (
	"bufio"
	"context"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/IrineSistiana/mosdns/v5/coremain"
	"github.com/IrineSistiana/mosdns/v5/pkg/matcher/netlist"
	"github.com/IrineSistiana/mosdns/v5/pkg/query_context"
	"github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
)

const PluginType = "client_map"

func init() {
	sequence.MustRegMatchQuickSetup(PluginType, QuickSetup)
}

// QuickSetup 解析 quick setup 参数并构建匹配器。
// 格式: "&<map_file> [&<map_file2>] <tag1> [tag2] ..."
func QuickSetup(_ sequence.BQ, s string) (sequence.Matcher, error) {
	args := parseArgs(s)
	if len(args.files) == 0 {
		return nil, fmt.Errorf("client_map: 至少需要一个映射文件（使用 & 前缀）")
	}
	if len(args.tags) == 0 {
		return nil, fmt.Errorf("client_map: 至少需要一个客户端组标签")
	}

	// 从映射文件加载 tag → []Prefix
	tagMap, err := loadMapFiles(args.files)
	if err != nil {
		return nil, fmt.Errorf("client_map: 加载映射文件失败: %w", err)
	}

	// 把请求的 tag 对应的所有 IP/CIDR 合并到一个 netlist
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

type quickSetupArgs struct {
	files []string
	tags  []string
}

func parseArgs(s string) *quickSetupArgs {
	args := &quickSetupArgs{}
	for _, field := range strings.Fields(s) {
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

// clientMapMatcher 实现 sequence.Matcher 和 fastMatcher 接口。
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
