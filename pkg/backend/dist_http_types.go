package backend

import (
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// Shared HTTP request/response DTOs for distributed transport/server.
type httpSetRequest struct { // shared with server & transport
	Key        string `json:"key"`
	Value      any    `json:"value"`
	Expiration int64  `json:"expiration_ms"`
	Version    uint64 `json:"version"`
	Origin     string `json:"origin"`
	Replicate  bool   `json:"replicate"`
}

type httpSetResponse struct {
	Error string `json:"error,omitempty"`
}

type httpGetResponse struct {
	Found bool        `json:"found"`
	Item  *cache.Item `json:"item,omitempty"`
	Error string      `json:"error,omitempty"`
}
