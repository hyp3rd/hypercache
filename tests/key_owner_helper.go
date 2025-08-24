// Package tests contains integration and helper utilities used across distributed
// backend tests (non-exported in main module). Lint requires a package comment.
package tests

import (
	"fmt"

	"github.com/hyp3rd/hypercache/internal/cluster"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// FindOwnerKey brute forces keys until it finds one whose owner ordering matches exactly ids.
func FindOwnerKey(b *backend.DistMemory, prefix string, desired []cluster.NodeID, limit int) (string, bool) { //nolint:ireturn
	for i := range limit {
		cand := fmt.Sprintf("%s%d", prefix, i)

		owners := b.Ring().Lookup(cand)
		if len(owners) != len(desired) {
			continue
		}

		match := true
		for j := range owners {
			if owners[j] != desired[j] {
				match = false

				break
			}
		}

		if match {
			return cand, true
		}
	}

	return "", false
}
