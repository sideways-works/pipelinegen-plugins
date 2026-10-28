//go:build unix

package collect

import (
	"io/fs"
	"syscall"
)

func inode(info fs.FileInfo) (uint64, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}

	return uint64(st.Ino), true //nolint:unconvert // a uint32 on some systems
}
