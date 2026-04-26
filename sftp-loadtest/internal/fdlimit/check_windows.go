//go:build windows

// Windows does not expose a unix-style RLIMIT_NOFILE and its per-process
// handle budget is high enough by default that no startup tuning is needed.
package fdlimit

func Check() {}
