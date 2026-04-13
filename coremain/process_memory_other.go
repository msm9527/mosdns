//go:build !linux

package coremain

func disableProcessTransparentHugePages() (bool, error) {
	return false, nil
}
