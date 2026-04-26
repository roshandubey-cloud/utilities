//go:build !windows

package proc

import (
	"os"
	"runtime"
)

// fdInUse returns the number of open file descriptors held by this process.
// /proc/self/fd on Linux, /dev/fd on Darwin / BSDs. Uses Open+Readdirnames
// rather than os.ReadDir to avoid an fstatat on the parent path, which
// fails with EBADF on darwin daemons whose stdin isn't a tty.
func fdInUse() int64 {
	path := "/dev/fd"
	if runtime.GOOS == "linux" {
		path = "/proc/self/fd"
	}
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return -1
	}
	n := int64(len(names)) - 1
	if n < 0 {
		return 0
	}
	return n
}
