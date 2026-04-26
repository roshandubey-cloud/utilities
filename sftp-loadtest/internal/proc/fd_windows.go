//go:build windows

package proc

// Windows uses a per-process handle table, not unix-style FDs; reporting
// "FDs in use" doesn't translate cleanly. Return -1 so the UI shows "—".
func fdInUse() int64 { return -1 }
