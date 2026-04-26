//go:build linux

package hostinfo

import (
	"os"
	"strconv"
	"strings"
)

// totalRAMMB reads MemTotal from /proc/meminfo. Returns 0 on error.
func totalRAMMB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb / 1024
	}
	return 0
}

// ifSpeedMbps reads /sys/class/net/<name>/speed when present (link speed in
// megabits/s). Returns 0 if the file is missing (virtual interfaces, WiFi
// where the value is "-1", etc.) so the UI can show "—".
func ifSpeedMbps(name string) int64 {
	data, err := os.ReadFile("/sys/class/net/" + name + "/speed")
	if err != nil {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}
