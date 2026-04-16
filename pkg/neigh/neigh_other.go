//go:build !linux

package neigh

import "errors"

// ReadAll 在非 Linux 平台返回错误（邻居表自动发现仅支持 Linux）。
func ReadAll() ([]Entry, error) {
	return nil, errors.New("neigh: 邻居表自动发现仅支持 Linux 平台")
}
