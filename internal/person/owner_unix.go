//go:build !windows

package person

import (
	"os"
	"syscall"
)

func ownedByRoot(st os.FileInfo) bool {
	s, ok := st.Sys().(*syscall.Stat_t)
	return ok && s.Uid == 0
}
