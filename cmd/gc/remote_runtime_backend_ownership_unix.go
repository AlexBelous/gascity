//go:build unix

package main

import (
	"os"
	"syscall"
)

func remoteFileOwnership(info os.FileInfo) (uint32, uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return stat.Uid, uint64(stat.Nlink), true //nolint:unconvert // Darwin Nlink is uint16; normalize the cross-platform identity.
}
