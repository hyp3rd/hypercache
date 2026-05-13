package backend

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// recordingTransport counts ForwardSet invocations so chaos tests
// can assert "the inner transport was reached" or "the inner
// transport was NOT reached" depending on whether the chaos drop
// fired. The other DistTransport methods are stubs — chaos
// applies the same shape to every method, so testing one verb is
// enough.
type recordingTransport struct {
	calls atomic.Int64
}

func (r *recordingTransport) ForwardSet(_ context.Context, _ string, _ *cache.Item, _ bool) error {
	r.calls.Add(1)

	return nil
}

func (*recordingTransport) ForwardGet(_ context.Context, _, _ string) (*cache.Item, bool, error) {
	return nil, false, nil
}

func (*recordingTransport) ForwardRemove(_ context.Context, _, _ string, _ bool) error {
	return nil
}

func (*recordingTransport) Health(_ context.Context, _ string) error { return nil }
func (*recordingTransport) IndirectHealth(_ context.Context, _, _ string) error {
	return nil
}

func (*recordingTransport) Gossip(_ context.Context, _ string, _ []GossipMember) error {
	return nil
}

func (*recordingTransport) FetchMerkle(_ context.Context, _ string) (*MerkleTree, error) {
	return nil, nil //nolint:nilnil // stub never invoked by chaos unit tests
}

func (*recordingTransport) ListKeys(_ context.Context, _, _ string) ([]string, error) {
	return nil, nil
}

// TestChaos_DropRateOneAlwaysDrops pins that DropRate=1.0 short-
// circuits every transport call with ErrChaosDrop. The inner
// transport must NOT be invoked.
func TestChaos_DropRateOneAlwaysDrops(t *testing.T) {
	t.Parallel()

	chaos := NewChaos()
	chaos.SetDropRate(1.0)

	inner := &recordingTransport{}
	wrapped := newChaosTransport(inner, chaos)

	const calls = 5

	for range calls {
		err := wrapped.ForwardSet(context.Background(), "peer", &cache.Item{Key: "k"}, false)
		if !errors.Is(err, ErrChaosDrop) {
			t.Fatalf("want ErrChaosDrop, got %v", err)
		}
	}

	if inner.calls.Load() != 0 {
		t.Errorf("inner transport reached %d times; want 0", inner.calls.Load())
	}

	if chaos.Drops() != int64(calls) {
		t.Errorf("Drops counter = %d, want %d", chaos.Drops(), calls)
	}
}

// TestChaos_DropRateZeroNeverDrops pins the inverse: with chaos
// configured but DropRate=0, every call passes through to the
// inner transport. The chaos overhead path is exercised; the
// drop branch never fires.
func TestChaos_DropRateZeroNeverDrops(t *testing.T) {
	t.Parallel()

	chaos := NewChaos()
	chaos.SetDropRate(0)

	inner := &recordingTransport{}
	wrapped := newChaosTransport(inner, chaos)

	const calls = 50

	for range calls {
		err := wrapped.ForwardSet(context.Background(), "peer", &cache.Item{Key: "k"}, false)
		if err != nil {
			t.Fatalf("unexpected err with DropRate=0: %v", err)
		}
	}

	if inner.calls.Load() != int64(calls) {
		t.Errorf("inner transport reached %d times; want %d", inner.calls.Load(), calls)
	}

	if chaos.Drops() != 0 {
		t.Errorf("Drops counter = %d, want 0", chaos.Drops())
	}
}

// TestChaos_LatencyFiresAndDelaysCall confirms latency injection
// holds the call for the configured duration. The probabilistic
// rate is set to 1.0 so every call gets latency; we measure
// wall-clock and verify it's at least the configured floor.
func TestChaos_LatencyFiresAndDelaysCall(t *testing.T) {
	t.Parallel()

	chaos := NewChaos()
	chaos.SetLatency(20*time.Millisecond, 1.0)

	inner := &recordingTransport{}
	wrapped := newChaosTransport(inner, chaos)

	start := time.Now()

	err := wrapped.ForwardSet(context.Background(), "peer", &cache.Item{Key: "k"}, false)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}

	elapsed := time.Since(start)
	// Allow a small floor below 20ms in case time.Sleep returns a
	// hair early on some schedulers; the key assertion is "latency
	// was clearly injected", not "exactly 20ms".
	if elapsed < 15*time.Millisecond {
		t.Errorf("elapsed = %v, want >= ~20ms (latency should have fired)", elapsed)
	}

	if chaos.Latencies() != 1 {
		t.Errorf("Latencies counter = %d, want 1", chaos.Latencies())
	}
}

// TestChaos_NilChaosIsNoop pins the disabled path: passing a nil
// Chaos to newChaosTransport returns the inner transport unwrapped
// so callers that don't configure chaos pay zero overhead.
func TestChaos_NilChaosIsNoop(t *testing.T) {
	t.Parallel()

	inner := &recordingTransport{}

	wrapped := newChaosTransport(inner, nil)
	if wrapped != inner {
		t.Fatalf("nil chaos should return inner unchanged; got distinct wrapper")
	}
}

// TestChaos_DisabledChaosLeavesCallsUntouched pins that a Chaos
// with zero DropRate AND zero LatencyRate is functionally a
// pass-through even when the wrapper is installed.
func TestChaos_DisabledChaosLeavesCallsUntouched(t *testing.T) {
	t.Parallel()

	chaos := NewChaos() // all zero
	inner := &recordingTransport{}
	wrapped := newChaosTransport(inner, chaos)

	const calls = 100

	for range calls {
		_ = wrapped.ForwardSet(context.Background(), "peer", &cache.Item{Key: "k"}, false)
	}

	if inner.calls.Load() != int64(calls) {
		t.Errorf("inner transport reached %d times; want %d", inner.calls.Load(), calls)
	}

	if chaos.Drops() != 0 || chaos.Latencies() != 0 {
		t.Errorf("disabled chaos should not increment counters: drops=%d latencies=%d",
			chaos.Drops(), chaos.Latencies())
	}
}

// TestChaos_ConcurrentCallsAreRaceFree drives many goroutines
// through the chaos wrapper simultaneously with both faults
// configured. Run under `-race`; failure manifests as the race
// detector firing, not a content assertion.
func TestChaos_ConcurrentCallsAreRaceFree(t *testing.T) {
	t.Parallel()

	chaos := NewChaos()
	chaos.SetDropRate(0.3)
	chaos.SetLatency(1*time.Millisecond, 0.3)

	inner := &recordingTransport{}
	wrapped := newChaosTransport(inner, chaos)

	const (
		goroutines        = 8
		callsPerGoroutine = 50
	)

	var wg sync.WaitGroup

	for range goroutines {
		wg.Go(func() {
			for range callsPerGoroutine {
				_ = wrapped.ForwardSet(context.Background(), "peer", &cache.Item{Key: "k"}, false)
			}
		})
	}

	wg.Wait()

	total := chaos.Drops() + inner.calls.Load()
	want := int64(goroutines * callsPerGoroutine)

	if total != want {
		t.Errorf("drops+inner_calls = %d, want %d (drops=%d, inner=%d)",
			total, want, chaos.Drops(), inner.calls.Load())
	}
}

// TestChaos_SetDropRateClampsRange pins the boundary check: values
// outside [0, 1] get clamped rather than silently producing
// undefined behavior. Tests that pass 1.5 by accident should get
// 100% drop, not garbage.
func TestChaos_SetDropRateClampsRange(t *testing.T) {
	t.Parallel()

	chaos := NewChaos()

	chaos.SetDropRate(-0.5)

	// At drop=0 every call passes.
	inner := &recordingTransport{}
	wrapped := newChaosTransport(inner, chaos)

	_ = wrapped.ForwardSet(context.Background(), "p", &cache.Item{Key: "k"}, false)

	if chaos.Drops() != 0 {
		t.Errorf("DropRate(-0.5) should clamp to 0; drops=%d", chaos.Drops())
	}

	chaos.SetDropRate(1.5)

	_ = wrapped.ForwardSet(context.Background(), "p", &cache.Item{Key: "k"}, false)

	if chaos.Drops() != 1 {
		t.Errorf("DropRate(1.5) should clamp to 1.0 (always drop); drops=%d", chaos.Drops())
	}
}

// TestChaos_NilReceiverIsSafe documents the nil-Chaos contract:
// methods called on a nil *Chaos must not panic. This matters
// because DistMemory.chaos may be nil when chaos isn't configured,
// and the Metrics() snapshot path calls Drops() / Latencies()
// unconditionally.
func TestChaos_NilReceiverIsSafe(t *testing.T) {
	t.Parallel()

	var c *Chaos

	if got := c.Drops(); got != 0 {
		t.Errorf("nil.Drops() = %d, want 0", got)
	}

	if got := c.Latencies(); got != 0 {
		t.Errorf("nil.Latencies() = %d, want 0", got)
	}

	// Mutators on nil are silent no-ops; calling these would
	// panic without the nil guard in SetDropRate / SetLatency.
	c.SetDropRate(0.5)
	c.SetLatency(time.Second, 1.0)
}
