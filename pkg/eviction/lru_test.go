package eviction

// LRU-specific tests. The IAlgorithm contract (basic Set/Get/Delete/Evict
// semantics, capacity bounds, zero-capacity no-op) is shared with CAWOLFU
// and lives in contract_test.go (TestLRUContract).
//
// Add LRU-specific tests here when LRU's recency-ordered behavior diverges
// from CAWOLFU's frequency-ordered behavior in ways the contract can't
// express (e.g. "after Get(a), Get(b), Set(c), the victim is a, not b").
