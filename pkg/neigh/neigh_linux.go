//go:build linux

package neigh

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
)

// ReadAll 读取系统邻居表，返回所有有效的 IP↔MAC 条目。
// 仅保留状态为 REACHABLE、STALE、DELAY、PROBE、PERMANENT 的条目，
// 过滤掉 INCOMPLETE、FAILED 等无效条目。
func ReadAll() ([]Entry, error) {
	neighbors, err := netlink.NeighList(0, 0) // 0 = all interfaces, 0 = all families
	if err != nil {
		return nil, fmt.Errorf("neigh: 读取邻居表失败: %w", err)
	}

	entries := make([]Entry, 0, len(neighbors))
	for _, n := range neighbors {
		// 过滤无效状态
		if !isValidState(n.State) {
			continue
		}
		if n.IP == nil || len(n.HardwareAddr) == 0 {
			continue
		}
		// 过滤全零 MAC（未完成的条目）
		if isZeroMAC(n.HardwareAddr) {
			continue
		}

		addr, ok := toNetipAddr(n.IP)
		if !ok {
			continue
		}

		entries = append(entries, Entry{
			IP:  addr,
			MAC: n.HardwareAddr.String(),
		})
	}
	return entries, nil
}

// isValidState 判断邻居条目是否处于有效状态。
func isValidState(state int) bool {
	// 来自 linux/neighbour.h
	const (
		nudReachable  = 0x02
		nudStale      = 0x04
		nudDelay      = 0x08
		nudProbe      = 0x10
		nudPermanent  = 0x80
		validStateMask = nudReachable | nudStale | nudDelay | nudProbe | nudPermanent
	)
	return state&validStateMask != 0
}

func isZeroMAC(mac net.HardwareAddr) bool {
	for _, b := range mac {
		if b != 0 {
			return false
		}
	}
	return true
}

func toNetipAddr(ip net.IP) (netip.Addr, bool) {
	if ip4 := ip.To4(); ip4 != nil {
		return netip.AddrFrom4([4]byte(ip4)), true
	}
	if len(ip) == net.IPv6len {
		return netip.AddrFrom16([16]byte(ip)), true
	}
	return netip.Addr{}, false
}
