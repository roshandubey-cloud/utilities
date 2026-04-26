//go:build !windows

package hostinfo

import (
	"os"
	"runtime"
	"syscall"
)

// fdLimits returns RLIMIT_NOFILE soft + hard. (0, 0) on error.
func fdLimits() (soft, hard int64) {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil {
		return 0, 0
	}
	return int64(rl.Cur), int64(rl.Max)
}

// fdInUse counts the calling process's open descriptors. On Linux this is
// /proc/self/fd. On Darwin and BSDs it's /dev/fd — but on Darwin os.ReadDir
// triggers an fstatat on the magic path that fails with EBADF when stdin
// isn't a real tty (i.e. exactly the case for daemons started with `&` /
// `disown`). So we use Open + Readdirnames, which doesn't stat the dir.
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
	n := int64(len(names)) - 1 // discount the dir-fd we just opened
	if n < 0 {
		return 0
	}
	return n
}
