//go:build darwin

package hostinfo

import "golang.org/x/sys/unix"

// totalRAMMB asks the kernel for hw.memsize. unix.SysctlUint64 handles the
// 8-byte LE decode and (critically) doesn't strip trailing NUL bytes the
// way syscall.Sysctl does — important because hw.memsize for a typical
// machine ends in several zero bytes.
func totalRAMMB() int64 {
	v, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return int64(v / (1024 * 1024))
}

// ifSpeedMbps is a no-op on darwin — there's no portable kernel knob and
// scraping ifconfig output in a daemon is not worth the maintenance cost.
func ifSpeedMbps(name string) int64 { return 0 }
