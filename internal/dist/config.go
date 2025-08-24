package dist

import "time"

const (
	defaultVirtualNodes = 64
)

// Config holds cluster node + distributed settings for DistMemory (and future networked backends).
type Config struct {
	NodeID           string
	BindAddr         string // address the node listens on for RPC
	AdvertiseAddr    string // address shared with peers (may differ from BindAddr)
	Seeds            []string
	Replication      int
	VirtualNodes     int
	ReadConsistency  int // maps to backend.ConsistencyLevel
	WriteConsistency int
	HintTTL          time.Duration
	HintReplay       time.Duration
	HintMaxPerNode   int
}

// Defaults returns a Config with safe initial values.
func Defaults() Config { //nolint:ireturn
	return Config{
		Replication:      1,
		VirtualNodes:     defaultVirtualNodes,
		ReadConsistency:  0, // ONE
		WriteConsistency: 1, // QUORUM (match backend default)
		HintTTL:          0,
		HintReplay:       0,
		HintMaxPerNode:   0,
	}
}
