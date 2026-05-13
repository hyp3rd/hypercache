package backend

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"math"
	mathrand "math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyp3rd/ewrap"

	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// ErrChaosDrop is the sentinel a chaos-wrapped transport returns
// when a configured DropRate triggers on a request. Tests use
// errors.Is(err, ErrChaosDrop) to confirm the fault path fired
// rather than a real transport failure.
var ErrChaosDrop = ewrap.New("dist transport: chaos drop")

// Chaos configures fault injection for resilience testing of the
// dist transport. Construct via NewChaos, configure via the
// SetDropRate / SetLatency helpers (atomic — safe to call from
// running tests), then pass to WithDistChaos when constructing
// the DistMemory under test.
//
// Disabled by default (zero values). When set on a DistMemory,
// every dist transport call goes through the chaos wrapper; the
// overhead is two atomic loads on the no-fault path so the cost
// stays trivial even when chaos is enabled but inactive.
//
// Chaos is for tests only. The hooks DO NOT belong in production
// — there's no safety mechanism preventing an operator from
// pointing a production cluster at a Chaos with DropRate=1.0.
// Don't.
type Chaos struct {
	// dropRate stores a float64 (0.0..1.0) in its bit-pattern form
	// so updates remain atomic. Read via math.Float64frombits.
	dropRate atomic.Uint64

	// latencyNs is the injected latency in nanoseconds. Zero means
	// latency injection is disabled.
	latencyNs atomic.Int64

	// latencyRate stores a float64 (0.0..1.0) same shape as dropRate.
	latencyRate atomic.Uint64

	// Telemetry: drops fires every time a call is dropped;
	// latencies fires every time latency is injected. Test code
	// asserts these to confirm chaos actually intercepted calls.
	drops     atomic.Int64
	latencies atomic.Int64

	// rng + rngMu produce the per-call probability rolls. Seeded
	// once from crypto/rand at construction; same pattern as the
	// SDK's failover shuffler so a fleet of chaos-enabled tests
	// don't synchronize their fault decisions.
	rng   *mathrand.Rand
	rngMu sync.Mutex
}

// NewChaos returns a zeroed Chaos. Configure via SetDropRate /
// SetLatency before or after passing to WithDistChaos; mutation
// is atomic and safe to interleave with running tests.
func NewChaos() *Chaos {
	var seed [16]byte

	_, err := cryptorand.Read(seed[:])
	if err != nil {
		// Crypto-rand failure is effectively impossible; fall
		// back to a stable seed so chaos still runs (poorly).
		seed = [16]byte{17, 31, 47, 61, 71, 83, 97, 109, 113, 127, 139, 151, 163, 173, 181, 191}
	}

	src := mathrand.NewPCG(
		binary.LittleEndian.Uint64(seed[:8]),
		binary.LittleEndian.Uint64(seed[8:]),
	)

	// G404: this is test-only fault-injection, not a security
	// surface — math/rand crypto-seeded from crypto/rand is the
	// standard recipe.
	// #nosec G404 -- test-fault-injection roll, seeded from crypto/rand
	return &Chaos{rng: mathrand.New(src)}
}

// SetDropRate configures the probability (0.0..1.0) that a single
// transport call is dropped instead of forwarded. Pass 0 to
// disable. Values outside [0, 1] are clamped.
func (c *Chaos) SetDropRate(p float64) {
	if c == nil {
		return
	}

	switch {
	case p < 0:
		p = 0
	case p > 1:
		p = 1
	default:
		// already in [0, 1]; no clamp needed
	}

	c.dropRate.Store(math.Float64bits(p))
}

// SetLatency configures injected latency (duration d) applied
// with the given probability rate (0.0..1.0). Pass d=0 OR rate=0
// to disable. Values outside [0, 1] are clamped.
func (c *Chaos) SetLatency(d time.Duration, rate float64) {
	if c == nil {
		return
	}

	switch {
	case rate < 0:
		rate = 0
	case rate > 1:
		rate = 1
	default:
		// already in [0, 1]; no clamp needed
	}

	c.latencyNs.Store(d.Nanoseconds())
	c.latencyRate.Store(math.Float64bits(rate))
}

// Drops returns the count of transport calls dropped since
// construction. Useful for test assertions of the shape "assert
// chaos.Drops() > 0 after running the load".
func (c *Chaos) Drops() int64 {
	if c == nil {
		return 0
	}

	return c.drops.Load()
}

// Latencies returns the count of transport calls that had latency
// injected since construction.
func (c *Chaos) Latencies() int64 {
	if c == nil {
		return 0
	}

	return c.latencies.Load()
}

// roll returns a fresh [0, 1) float for a probability decision.
// Single goroutine-safe path; tests under -race exercise this via
// concurrent transport calls.
func (c *Chaos) roll() float64 {
	c.rngMu.Lock()
	defer c.rngMu.Unlock()

	return c.rng.Float64()
}

// maybeDrop returns ErrChaosDrop with probability dropRate; nil
// otherwise. Called at the start of every chaos-wrapped transport
// method.
func (c *Chaos) maybeDrop() error {
	if c == nil {
		return nil
	}

	p := math.Float64frombits(c.dropRate.Load())
	if p <= 0 {
		return nil
	}

	if c.roll() < p {
		c.drops.Add(1)

		return ErrChaosDrop
	}

	return nil
}

// maybeLatency sleeps for the configured latency with probability
// latencyRate. Returns immediately when chaos is disabled or the
// roll fails.
func (c *Chaos) maybeLatency() {
	if c == nil {
		return
	}

	rate := math.Float64frombits(c.latencyRate.Load())
	if rate <= 0 {
		return
	}

	ns := c.latencyNs.Load()
	if ns <= 0 {
		return
	}

	if c.roll() < rate {
		c.latencies.Add(1)
		time.Sleep(time.Duration(ns))
	}
}

// chaosTransport wraps a DistTransport, applying configured
// faults before delegating each method to the inner transport.
// Constructed automatically by storeTransport when the DistMemory
// has a non-nil Chaos configured; user code doesn't construct it.
type chaosTransport struct {
	inner DistTransport
	chaos *Chaos
}

// newChaosTransport wraps inner with the given chaos config. A
// nil chaos returns inner unchanged so callers don't have to
// branch on the disabled case.
func newChaosTransport(inner DistTransport, chaos *Chaos) DistTransport {
	if chaos == nil || inner == nil {
		return inner
	}

	return &chaosTransport{inner: inner, chaos: chaos}
}

// ForwardSet, ForwardGet, ForwardRemove apply chaos then delegate.
// Identical shape for every method — the boilerplate is the cost
// of wrapping each interface method individually rather than
// using reflection (which would slow the hot path).
func (t *chaosTransport) ForwardSet(ctx context.Context, nodeID string, item *cache.Item, replicate bool) error {
	err := t.applyFault()
	if err != nil {
		return err
	}

	return t.inner.ForwardSet(ctx, nodeID, item, replicate)
}

func (t *chaosTransport) ForwardGet(ctx context.Context, nodeID, key string) (*cache.Item, bool, error) {
	err := t.applyFault()
	if err != nil {
		return nil, false, err
	}

	return t.inner.ForwardGet(ctx, nodeID, key)
}

func (t *chaosTransport) ForwardRemove(ctx context.Context, nodeID, key string, replicate bool) error {
	err := t.applyFault()
	if err != nil {
		return err
	}

	return t.inner.ForwardRemove(ctx, nodeID, key, replicate)
}

func (t *chaosTransport) Health(ctx context.Context, nodeID string) error {
	err := t.applyFault()
	if err != nil {
		return err
	}

	return t.inner.Health(ctx, nodeID)
}

func (t *chaosTransport) IndirectHealth(ctx context.Context, relayNodeID, targetNodeID string) error {
	err := t.applyFault()
	if err != nil {
		return err
	}

	return t.inner.IndirectHealth(ctx, relayNodeID, targetNodeID)
}

func (t *chaosTransport) Gossip(ctx context.Context, targetNodeID string, members []GossipMember) error {
	err := t.applyFault()
	if err != nil {
		return err
	}

	return t.inner.Gossip(ctx, targetNodeID, members)
}

func (t *chaosTransport) ListKeys(ctx context.Context, nodeID, pattern string) ([]string, error) {
	err := t.applyFault()
	if err != nil {
		return nil, err
	}

	return t.inner.ListKeys(ctx, nodeID, pattern)
}

func (t *chaosTransport) FetchMerkle(ctx context.Context, nodeID string) (*MerkleTree, error) {
	err := t.applyFault()
	if err != nil {
		return nil, err
	}

	return t.inner.FetchMerkle(ctx, nodeID)
}

// applyFault is the canonical pre-call hook: maybeDrop (return
// error to short-circuit) then maybeLatency (sleep). Every
// DistTransport method invokes this before delegating. Placed
// after the exported-style methods per the funcorder lint —
// unexported helpers come last in the receiver's method block.
func (t *chaosTransport) applyFault() error {
	err := t.chaos.maybeDrop()
	if err != nil {
		return err
	}

	t.chaos.maybeLatency()

	return nil
}

// WithDistChaos enables chaos injection on the dist transport.
// Pass a *Chaos constructed via NewChaos; configure faults via
// the Chaos's SetDropRate / SetLatency methods.
//
// Option ordering does not matter: WithDistChaos records the
// reference, and storeTransport wraps the active transport
// whenever it's set (including the auto-wired HTTP transport).
//
// FOR TESTS ONLY. There's no safety check preventing this from
// being applied to a production DistMemory — pointing a real
// cluster at a Chaos with DropRate=1.0 will drop every dist
// transport call. Don't.
func WithDistChaos(c *Chaos) DistMemoryOption {
	return func(dm *DistMemory) {
		dm.chaos = c
		// If a transport is already configured (option ordering:
		// WithDistTransport before WithDistChaos), re-wrap it now
		// so chaos is in effect immediately.
		current := dm.loadTransport()
		if current != nil {
			if _, alreadyWrapped := current.(*chaosTransport); !alreadyWrapped {
				dm.storeTransport(newChaosTransport(current, c))
			}
		}
	}
}
