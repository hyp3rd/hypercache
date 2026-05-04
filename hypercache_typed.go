package hypercache

import (
	"context"
	"time"

	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// Typed is a thin wrapper around HyperCache that adds compile-time
// type-safety on Set/Get/GetOrSet/GetMultiple/GetWithInfo without
// breaking the existing untyped API. Internally Item.Value is still
// stored as any; the wrapper performs a single type assertion on read
// and rejects cross-typed entries as ErrTypeMismatch.
//
// See docs/rfcs/0002-generic-item-typing.md for the full design.
//
// Lifecycle: Typed does NOT own the underlying HyperCache. Callers are
// responsible for calling hc.Stop themselves; multiple Typed instances
// of different V parameters can share a single underlying cache.
//
// Discipline: do not mix Typed[V1] and Typed[V2] (or Typed and untyped)
// reads against the same key — the wrapper treats wrong-type entries as
// ErrTypeMismatch on Get, but the storage layer has no notion of V.
type Typed[T backend.IBackendConstrain, V any] struct {
	hc *HyperCache[T]
}

// NewTyped wraps an existing HyperCache instance to provide the typed
// surface. The underlying cache continues to function unchanged for any
// callers that hold a reference to it.
func NewTyped[T backend.IBackendConstrain, V any](hc *HyperCache[T]) *Typed[T, V] {
	return &Typed[T, V]{hc: hc}
}

// Underlying returns the wrapped HyperCache. Use it for operations the
// typed wrapper deliberately does not expose (List, Stop, Capacity,
// stats, etc.) — those operate on metadata that is V-agnostic and
// adding type parameters would only obscure the call site.
func (t *Typed[T, V]) Underlying() *HyperCache[T] { return t.hc }

// Set stores value under key. expiration ≤ 0 means no expiry.
// Mirrors HyperCache.Set; the V constraint enforces the value type at
// compile time so callers cannot accidentally Set the wrong shape.
func (t *Typed[T, V]) Set(ctx context.Context, key string, value V, expiration time.Duration) error {
	return t.hc.Set(ctx, key, value, expiration)
}

// Get retrieves the typed value for key. Returns the zero value and
// false on miss, expired entry, or type mismatch — the latter is
// treated as a miss so a single Get can't panic on a stale heterogeneous
// entry. Use GetTyped if the caller needs to distinguish miss from
// type-mismatch.
func (t *Typed[T, V]) Get(ctx context.Context, key string) (V, bool) {
	var zero V

	raw, ok := t.hc.Get(ctx, key)
	if !ok {
		return zero, false
	}

	v, ok := raw.(V)
	if !ok {
		return zero, false
	}

	return v, true
}

// GetTyped is the explicit-error variant of Get. Returns:
//   - (V, nil)              on hit
//   - (zero, ErrKeyNotFound) on miss / expired
//   - (zero, ErrTypeMismatch) when the stored value is not assertable to V
//
// Callers who want to surface "wrong type was stored under this key"
// (e.g., as an alerting signal that a Typed[V1] and Typed[V2] are
// fighting over the same key) should use this form.
func (t *Typed[T, V]) GetTyped(ctx context.Context, key string) (V, error) {
	var zero V

	raw, ok := t.hc.Get(ctx, key)
	if !ok {
		return zero, sentinel.ErrKeyNotFound
	}

	v, ok := raw.(V)
	if !ok {
		return zero, sentinel.ErrTypeMismatch
	}

	return v, nil
}

// GetWithInfo returns the cache.Item plus the typed value. The Item is
// returned as-is (Value still typed as any); use the second return for
// the V-typed value when assignment cleanliness matters. Returns
// (nil, zero, false) on miss / expired / type-mismatch (same fail-soft
// semantics as Get).
func (t *Typed[T, V]) GetWithInfo(ctx context.Context, key string) (*cache.Item, V, bool) {
	var zero V

	item, ok := t.hc.GetWithInfo(ctx, key)
	if !ok {
		return nil, zero, false
	}

	v, ok := item.Value.(V)
	if !ok {
		return nil, zero, false
	}

	return item, v, true
}

// GetOrSet returns the existing value when present, or stores value and
// returns it. If the existing entry is the wrong type, the wrapper
// surfaces ErrTypeMismatch rather than overwriting (avoids silent
// data loss when two callers fight over a key).
func (t *Typed[T, V]) GetOrSet(ctx context.Context, key string, value V, expiration time.Duration) (V, error) {
	var zero V

	raw, err := t.hc.GetOrSet(ctx, key, value, expiration)
	if err != nil {
		return zero, err
	}

	v, ok := raw.(V)
	if !ok {
		return zero, sentinel.ErrTypeMismatch
	}

	return v, nil
}

// GetMultiple is the typed analog of HyperCache.GetMultiple. Hits land
// in the result map under their key; misses (sentinel.ErrKeyNotFound)
// and type mismatches (sentinel.ErrTypeMismatch) land in the error map.
// Mirrors the dual-map shape of the underlying API.
func (t *Typed[T, V]) GetMultiple(ctx context.Context, keys ...string) (map[string]V, map[string]error) {
	rawHits, errs := t.hc.GetMultiple(ctx, keys...)

	hits := make(map[string]V, len(rawHits))

	for k, raw := range rawHits {
		v, ok := raw.(V)
		if !ok {
			// Promote the type mismatch into the error map so the
			// caller sees a uniform success/failure split.
			errs[k] = sentinel.ErrTypeMismatch

			continue
		}

		hits[k] = v
	}

	return hits, errs
}

// Remove deletes one or more keys. V is unused but kept on the receiver
// for API symmetry — callers don't need to know which Typed instance
// owns a key to delete it.
func (t *Typed[T, V]) Remove(ctx context.Context, keys ...string) error {
	return t.hc.Remove(ctx, keys...)
}

// Clear removes every entry from the underlying cache. Affects entries
// stored by other Typed instances and untyped callers — it operates on
// the storage layer, not on V.
func (t *Typed[T, V]) Clear(ctx context.Context) error {
	return t.hc.Clear(ctx)
}
