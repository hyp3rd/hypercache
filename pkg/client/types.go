package client

import "slices"

// Identity is the resolved caller — the response from GET /v1/me.
// Identity values are short-lived; treat them as a snapshot of "who
// am I right now against this cluster?" rather than a persistent
// principal.
//
// Capabilities is the stable surface clients should key off. Scopes
// is preserved on the type for parity with the server's view but
// may be empty when a future server splits scopes into multiple
// capabilities — see the wire docs at /v1/me for the migration
// contract.
type Identity struct {
	// ID is the human-readable identifier the operator assigned
	// (e.g. "svc-billing", "ops-readonly", "anonymous"). Stable
	// across credential rotations as long as the operator keeps
	// the same `id:` mapping in the auth config.
	ID string
	// Scopes is the raw scope strings from the server: "read",
	// "write", "admin". Order matches the server's slice.
	Scopes []string
	// Capabilities is the derived stable view: prefixed with
	// "cache." (e.g. "cache.read"). Prefer this for permission
	// checks — capability strings stay stable even if a scope is
	// later split.
	Capabilities []string
}

// HasCapability reports whether the identity carries the given
// capability string. The match is exact — capability strings are a
// closed taxonomy, not a hierarchy.
func (i Identity) HasCapability(name string) bool {
	return slices.Contains(i.Capabilities, name)
}

// Item is the full cached entry — what GetItem returns. Mirrors
// the server's wire ItemEnvelope shape. Value is always the decoded
// bytes (the wire's base64 is unwound by the client) so callers
// don't have to do encoding bookkeeping.
type Item struct {
	// Key is the cache key.
	Key string
	// Value is the cached bytes. Always raw bytes; the wire's
	// base64 envelope is decoded for callers.
	Value []byte
	// TTLMs is the time-to-live in milliseconds at the moment
	// the item was written. Zero means no expiry. Note this is
	// the TTL at write time, not the remaining lifetime — use
	// ExpiresAt for the latter.
	TTLMs int64
	// ExpiresAt is the absolute expiry timestamp as an RFC3339
	// string. Empty when the item has no TTL.
	ExpiresAt string
	// Version is the per-key Lamport version. Monotonically
	// increasing per key across all owners; useful for causality
	// reasoning and conflict detection.
	Version uint64
	// Origin is the node ID that originated this version. Stable
	// across the item's lifetime unless a conflict resolution
	// promotes a different write.
	Origin string
	// LastUpdated is the wall-clock timestamp of the last write,
	// formatted as RFC3339.
	LastUpdated string
	// Node is the node ID that served this request — useful for
	// debugging routing decisions and pinpointing flaky nodes.
	Node string
	// Owners is the ring's ownership list for this key. The first
	// entry is the primary; subsequent entries are replicas. Use
	// this to verify your direct-routing decisions (Phase 5.1
	// when the SDK ships M3) match the cluster's actual view.
	Owners []string
}

// itemEnvelope is the wire shape we unmarshal from the server.
// Kept separate from Item so the public type can be a clean Go
// struct (Value []byte) while the wire stays binary-safe (base64).
type itemEnvelope struct {
	Key           string   `json:"key"`
	Value         string   `json:"value"`
	ValueEncoding string   `json:"value_encoding"`
	TTLMs         int64    `json:"ttl_ms,omitempty"`
	ExpiresAt     string   `json:"expires_at,omitempty"`
	Version       uint64   `json:"version"`
	Origin        string   `json:"origin,omitempty"`
	LastUpdated   string   `json:"last_updated,omitempty"`
	Node          string   `json:"node"`
	Owners        []string `json:"owners"`
}

// meResponse is the wire shape of GET /v1/me. Mirrors the server's
// type but lives in the client so we depend on the JSON contract,
// not the server's struct.
type meResponse struct {
	ID           string   `json:"id"`
	Scopes       []string `json:"scopes"`
	Capabilities []string `json:"capabilities"`
}

// clusterMember is one row of GET /cluster/members — the response
// shape the topology refresh loop consumes. We only care about
// alive-or-suspect nodes' API addresses; the full membership
// snapshot has more fields we ignore here.
type clusterMember struct {
	ID      string `json:"id"`
	Address string `json:"address"` // host:port; topology refresh combines this with the request scheme
	State   string `json:"state"`   // alive | suspect | dead
}

type clusterMembersResponse struct {
	Members []clusterMember `json:"members"`
}

// errorEnvelope is the canonical 4xx/5xx body shape. Decoded into
// a *StatusError by classifyResponse.
type errorEnvelope struct {
	Code    string `json:"code"`
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}
