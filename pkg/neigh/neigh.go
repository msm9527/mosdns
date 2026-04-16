// Package neigh 读取系统邻居表（ARP/NDP），用于自动发现双栈设备。
package neigh

import "net/netip"

// Entry 表示邻居表中一条有效的 IP↔MAC 映射。
type Entry struct {
	IP  netip.Addr
	MAC string // MAC 地址的标准格式，如 "aa:bb:cc:dd:ee:ff"
}
