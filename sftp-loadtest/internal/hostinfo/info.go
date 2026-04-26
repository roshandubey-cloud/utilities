// Package hostinfo collects cheap, one-shot facts about the machine the
// load tester is running on. The point is to keep the operator honest about
// the *real* ceilings — file-descriptor budget, CPU count, RAM, network
// interfaces — instead of letting them discover them at failure time.
//
// Everything in this file is cross-platform. Per-OS bits (rlimit, total
// RAM, fd-in-use count) live in the build-tagged sibling files and return
// graceful zeros where unsupported.
package hostinfo

import (
	"net"
	"os"
	"runtime"
)

// Info is the snapshot returned by Snapshot. Zero values mean "unknown on
// this platform", not "0" — UI should render them as "—".
type Info struct {
	Hostname    string  `json:"hostname"`
	OS          string  `json:"os"`         // runtime.GOOS
	Arch        string  `json:"arch"`       // runtime.GOARCH
	GoVersion   string  `json:"go_version"`
	NumCPU      int     `json:"num_cpu"`    // logical cores
	TotalRAMMB  int64   `json:"total_ram_mb"`  // 0 = unknown
	FDLimitSoft int64   `json:"fd_limit_soft"` // 0 on windows
	FDLimitHard int64   `json:"fd_limit_hard"` // 0 on windows
	FDInUse     int64   `json:"fd_in_use"`     // -1 = unknown
	Interfaces  []NetIF `json:"interfaces"`
}

type NetIF struct {
	Name      string   `json:"name"`
	Addrs     []string `json:"addrs"`
	SpeedMbps int64    `json:"speed_mbps,omitempty"` // best-effort, linux only
	Up        bool     `json:"up"`
}

// Snapshot reads everything cheaply available right now. Safe to call
// repeatedly — no caching, but each call is sub-millisecond.
func Snapshot() Info {
	host, _ := os.Hostname()
	soft, hard := fdLimits()       // platform-specific
	inUse := fdInUse()             // platform-specific; -1 if unknown
	return Info{
		Hostname:    host,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		GoVersion:   runtime.Version(),
		NumCPU:      runtime.NumCPU(),
		TotalRAMMB:  totalRAMMB(),  // platform-specific
		FDLimitSoft: soft,
		FDLimitHard: hard,
		FDInUse:     inUse,
		Interfaces:  netInterfaces(),
	}
}

func netInterfaces() []NetIF {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	out := make([]NetIF, 0, len(ifs))
	for _, ni := range ifs {
		if ni.Flags&net.FlagLoopback != 0 {
			continue // skip loopback — clients care about external NICs
		}
		addrs, _ := ni.Addrs()
		var ips []string
		for _, a := range addrs {
			ips = append(ips, a.String())
		}
		if len(ips) == 0 {
			continue // skip down/unconfigured interfaces
		}
		out = append(out, NetIF{
			Name:      ni.Name,
			Addrs:     ips,
			SpeedMbps: ifSpeedMbps(ni.Name), // platform-specific
			Up:        ni.Flags&net.FlagUp != 0,
		})
	}
	return out
}
