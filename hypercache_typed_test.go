package hypercache_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/sentinel"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// session is a value type used to exercise Typed[*session] round-trips
// against a typed wrapper — represents a typical user-defined cache value
// (struct pointer, not a stdlib type).
type session struct {
	UserID string
	Token  string
}

// newTypedTestCache spins up an in-memory HyperCache + a Typed[T, V]
// wrapper sharing it. Returns both so tests can poke at the underlying
// cache for cross-typed scenarios.
func newTypedTestCache[V any](t *testing.T) (*hypercache.HyperCache[backend.InMemory], *hypercache.Typed[backend.InMemory, V]) {
	t.Helper()

	hc, err := hypercache.NewInMemoryWithDefaults(context.Background(), 100)
	require.NoError(t, err)

	t.Cleanup(func() { _ = hc.Stop(context.Background()) })

	return hc, hypercache.NewTyped[backend.InMemory, V](hc)
}

// TestTyped_RoundTrip is the happy-path: Set then Get returns the typed
// value with no caller-side type assertion.
func TestTyped_RoundTrip(t *testing.T) {
	t.Parallel()

	_, typed := newTypedTestCache[*session](t)

	want := &session{UserID: "u-42", Token: "tok-abc"}

	require.NoError(t, typed.Set(context.Background(), "session:42", want, time.Hour))

	got, ok := typed.Get(context.Background(), "session:42")
	require.True(t, ok, "Get should hit")
	require.Equal(t, want, got)
}

// TestTyped_RoundTrip_PrimitiveValue covers the [...] stdlib-typed case;
// users frequently cache []byte payloads and the wrapper must not require
// pointer-only V.
func TestTyped_RoundTrip_PrimitiveValue(t *testing.T) {
	t.Parallel()

	_, typed := newTypedTestCache[[]byte](t)

	want := []byte("hello, typed")
	require.NoError(t, typed.Set(context.Background(), "k", want, time.Hour))

	got, ok := typed.Get(context.Background(), "k")
	require.True(t, ok)
	require.Equal(t, want, got)
}

// TestTyped_GetMiss checks the zero-value-and-false return on miss.
func TestTyped_GetMiss(t *testing.T) {
	t.Parallel()

	_, typed := newTypedTestCache[string](t)

	got, ok := typed.Get(context.Background(), "no-such-key")
	require.False(t, ok)
	require.Empty(t, got, "miss should return zero V")
}

// TestTyped_TypeMismatchTreatedAsMiss verifies the wrapper's fail-soft
// semantics: when the underlying cache holds a value of a different
// type, Get returns (zero, false) rather than panicking.
func TestTyped_TypeMismatchTreatedAsMiss(t *testing.T) {
	t.Parallel()

	hc, typed := newTypedTestCache[*session](t)

	// Store an int via the underlying cache, then read via the typed
	// wrapper expecting *session.
	require.NoError(t, hc.Set(context.Background(), "k", 42, time.Hour))

	got, ok := typed.Get(context.Background(), "k")
	require.False(t, ok, "wrong-type read should miss, not panic")
	require.Nil(t, got)
}

// TestTyped_GetTypedSurfacesMismatch is the explicit-error counterpart:
// callers who want to distinguish miss from wrong-type use GetTyped.
func TestTyped_GetTypedSurfacesMismatch(t *testing.T) {
	t.Parallel()

	hc, typed := newTypedTestCache[*session](t)

	// Miss → ErrKeyNotFound.
	_, err := typed.GetTyped(context.Background(), "no-such-key")
	require.ErrorIs(t, err, sentinel.ErrKeyNotFound)

	// Wrong type stored → ErrTypeMismatch.
	require.NoError(t, hc.Set(context.Background(), "k", 42, time.Hour))

	_, err = typed.GetTyped(context.Background(), "k")
	require.ErrorIs(t, err, sentinel.ErrTypeMismatch)
}

// TestTyped_GetWithInfo verifies the metadata + typed-value triple
// return surface. *cache.Item carries the access count, expiration, etc.
func TestTyped_GetWithInfo(t *testing.T) {
	t.Parallel()

	_, typed := newTypedTestCache[string](t)

	require.NoError(t, typed.Set(context.Background(), "k", "v", time.Hour))

	item, value, ok := typed.GetWithInfo(context.Background(), "k")
	require.True(t, ok)
	require.NotNil(t, item)
	require.Equal(t, "v", value)
	require.Equal(t, "k", item.Key)
}

// TestTyped_GetOrSet covers both branches: existing value and freshly
// stored value.
func TestTyped_GetOrSet(t *testing.T) {
	t.Parallel()

	_, typed := newTypedTestCache[string](t)

	// First call stores the value.
	got, err := typed.GetOrSet(context.Background(), "k", "v1", time.Hour)
	require.NoError(t, err)
	require.Equal(t, "v1", got)

	// Second call returns the stored value, ignoring the new one.
	got, err = typed.GetOrSet(context.Background(), "k", "v2-not-stored", time.Hour)
	require.NoError(t, err)
	require.Equal(t, "v1", got)
}

// TestTyped_GetOrSetTypeMismatch verifies that GetOrSet refuses to
// silently overwrite a wrong-type entry.
func TestTyped_GetOrSetTypeMismatch(t *testing.T) {
	t.Parallel()

	hc, typed := newTypedTestCache[string](t)

	// Pre-populate with the wrong type.
	require.NoError(t, hc.Set(context.Background(), "k", 42, time.Hour))

	got, err := typed.GetOrSet(context.Background(), "k", "v", time.Hour)
	require.ErrorIs(t, err, sentinel.ErrTypeMismatch)
	require.Empty(t, got)
}

// TestTyped_GetMultiple covers the dual-map result shape with a mix of
// hits, misses, and type mismatches.
func TestTyped_GetMultiple(t *testing.T) {
	t.Parallel()

	hc, typed := newTypedTestCache[string](t)

	require.NoError(t, typed.Set(context.Background(), "k1", "v1", time.Hour))
	require.NoError(t, typed.Set(context.Background(), "k2", "v2", time.Hour))
	// Inject a wrong-type entry under k3 via the underlying cache.
	require.NoError(t, hc.Set(context.Background(), "k3", 42, time.Hour))

	hits, errs := typed.GetMultiple(context.Background(), "k1", "k2", "k3", "missing")

	require.Equal(t, map[string]string{"k1": "v1", "k2": "v2"}, hits)
	require.ErrorIs(t, errs["k3"], sentinel.ErrTypeMismatch)
	require.ErrorIs(t, errs["missing"], sentinel.ErrKeyNotFound)
}

// TestTyped_Remove verifies the wrapper does not need V on the
// delete path — Remove is V-agnostic.
func TestTyped_Remove(t *testing.T) {
	t.Parallel()

	_, typed := newTypedTestCache[string](t)

	require.NoError(t, typed.Set(context.Background(), "k", "v", time.Hour))
	require.NoError(t, typed.Remove(context.Background(), "k"))

	_, ok := typed.Get(context.Background(), "k")
	require.False(t, ok)
}

// TestTyped_SharedUnderlyingCache exercises the documented composition
// pattern: one HyperCache, two Typed views over disjoint keyspaces.
// Confirms each view sees its own keys and doesn't trip over the other's
// (the wrapper provides no namespace isolation; callers handle that).
func TestTyped_SharedUnderlyingCache(t *testing.T) {
	t.Parallel()

	hc, _ := newTypedTestCache[string](t) // discard the string view

	sessions := hypercache.NewTyped[backend.InMemory, *session](hc)
	tokens := hypercache.NewTyped[backend.InMemory, string](hc)

	require.NoError(t, sessions.Set(context.Background(), "session:42", &session{UserID: "u-42"}, time.Hour))
	require.NoError(t, tokens.Set(context.Background(), "token:abc", "secret", time.Hour))

	s, ok := sessions.Get(context.Background(), "session:42")
	require.True(t, ok)
	require.Equal(t, "u-42", s.UserID)

	tok, ok := tokens.Get(context.Background(), "token:abc")
	require.True(t, ok)
	require.Equal(t, "secret", tok)

	// Underlying() exposes the shared cache for users who need V-agnostic
	// operations like List or Stop.
	require.NotNil(t, sessions.Underlying())
	require.Same(t, sessions.Underlying(), tokens.Underlying())
}
