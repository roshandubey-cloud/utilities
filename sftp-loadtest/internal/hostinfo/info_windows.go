//go:build windows

package hostinfo

// Windows uses a per-process handle table rather than RLIMIT_NOFILE; the
// soft cap that bites unix daemons doesn't apply the same way. We return
// zeros for the unix-style metrics and let the UI render "—".

func fdLimits() (int64, int64) { return 0, 0 }
func fdInUse() int64           { return -1 }
func totalRAMMB() int64        { return 0 }
func ifSpeedMbps(string) int64 { return 0 }
