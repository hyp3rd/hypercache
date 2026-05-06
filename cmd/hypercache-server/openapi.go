package main

import _ "embed"

// openapiSpec is the raw YAML of the client API's OpenAPI 3.1
// specification, embedded at build time from the sibling
// `openapi.yaml` (Go's `embed` directive cannot traverse `..`,
// so the spec lives next to the binary it describes). The
// server serves it at `GET /v1/openapi.yaml` so clients can
// discover the API surface without out-of-band docs — and so
// a deployed cluster's declared contract can never drift from
// the binary running behind it.
//
// The drift test in `openapi_test.go` asserts that every fiber
// route registered by `registerClientRoutes` is documented in
// this spec, and vice-versa; CI keeps the two in sync.
//
//go:embed openapi.yaml
var openapiSpec []byte
