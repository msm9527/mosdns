//go:build linux

package coremain

import (
	"golang.org/x/sys/unix"
)

const keepProcessTHPEnv = "MOSDNS_KEEP_THP"

func disableProcessTransparentHugePages() (bool, error) {
	if envTruthy(keepProcessTHPEnv) {
		return false, nil
	}

	disabled, err := unix.PrctlRetInt(unix.PR_GET_THP_DISABLE, 0, 0, 0, 0)
	if err == nil && disabled == 1 {
		return false, nil
	}

	if err := unix.Prctl(unix.PR_SET_THP_DISABLE, 1, 0, 0, 0); err != nil {
		return false, err
	}
	return true, nil
}
