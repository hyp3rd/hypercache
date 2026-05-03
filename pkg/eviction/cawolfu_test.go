package eviction

// CAWOLFU-specific tests. The IAlgorithm contract (basic Set/Get/Delete/Evict
// semantics, capacity bounds, zero-capacity no-op) is shared with LRU and
// lives in contract_test.go (TestCAWOLFUContract).
//
// Add CAWOLFU-specific tests here when CAWOLFU's frequency-ordered behavior
// diverges from LRU's recency-ordered behavior in ways the contract can't
// express (e.g. count-tie ordering, scan-resistance edge cases).
