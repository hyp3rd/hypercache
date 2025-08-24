package cluster

import "sync/atomic"

// MembershipVersion tracks a monotonically increasing version for membership changes.
// Used to expose a cheap cluster epoch for clients/metrics.
type MembershipVersion struct { // holds membership epoch
	v atomic.Uint64
}

// Next increments and returns the next version.
func (mv *MembershipVersion) Next() uint64 { return mv.v.Add(1) }

// Get returns current version.
func (mv *MembershipVersion) Get() uint64 { return mv.v.Load() }
