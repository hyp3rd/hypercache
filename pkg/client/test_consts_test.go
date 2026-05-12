package client_test

// JSON wire-shape keys shared across test fixtures. The cache
// server's batch/me/etc. endpoints all use snake_case JSON keys;
// declaring them as constants once keeps goconst happy and means a
// future wire-shape rename only touches this file.
const (
	jsonKey     = "key"
	jsonStored  = "stored"
	jsonOwners  = "owners"
	jsonResults = "results"
	jsonNode    = "node"
)
