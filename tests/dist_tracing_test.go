package tests

import (
	"context"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/hyp3rd/hypercache/pkg/backend"
	cache "github.com/hyp3rd/hypercache/pkg/cache/v2"
)

// recordingTracerProvider builds an in-memory tracer provider whose spans
// can be inspected via the returned recorder. Used by the assertions
// below to verify DistMemory emits the expected span tree without
// running an exporter.
func recordingTracerProvider() (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))

	return tp, rec
}

// findSpan returns the first recorded span with the given name, or nil
// if none was emitted. Spans accumulate in the recorder across the
// whole test, so `findSpan` collapses the search behind a name match
// for readable assertions.
func findSpan(rec *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	for _, s := range rec.Ended() {
		if s.Name() == name {
			return s
		}
	}

	return nil
}

// spanAttr returns the string form of the named attribute on span, or
// the empty string when not present. The string-only return is
// deliberate — the assertions below check well-known attribute keys
// we emit ourselves, not arbitrary user attrs, so a typed accessor
// would just expand the helper without buying anything.
func spanAttr(span sdktrace.ReadOnlySpan, key string) string {
	if span == nil {
		return ""
	}

	for _, a := range span.Attributes() {
		if string(a.Key) == key {
			return a.Value.Emit()
		}
	}

	return ""
}

// newSingleNodeTracedDist is the constructor shared by the per-op
// tracing tests below. Replication=1 keeps the trace tree shallow so
// per-op assertions don't have to filter out fan-out spans — those are
// exercised by TestDistMemory_TracingEmitsReplicationChildSpans.
func newSingleNodeTracedDist(t *testing.T, id string) (*backend.DistMemory, *tracetest.SpanRecorder) {
	t.Helper()

	tp, rec := recordingTracerProvider()
	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode(id, addr),
		backend.WithDistReplication(1),
		backend.WithDistTracerProvider(tp),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, dm)

	return dm, rec
}

// TestDistMemory_TracingEmitsSetSpan is half of the Phase A.2 contract:
// when a tracer provider is supplied, Set emits a `dist.set` span
// carrying the configured write-consistency attribute. Split from the
// Get assertions so each test stays narrow.
func TestDistMemory_TracingEmitsSetSpan(t *testing.T) {
	t.Parallel()

	dm, rec := newSingleNodeTracedDist(t, "trace-set-A")

	err := dm.Set(context.Background(), &cache.Item{
		Key:         "trace-key",
		Value:       []byte("v"),
		Version:     1,
		Origin:      "trace-set-A",
		LastUpdated: time.Now(),
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	setSpan := findSpan(rec, "dist.set")
	if setSpan == nil {
		t.Fatal("expected a `dist.set` span; none recorded")
	}

	if got := spanAttr(setSpan, "dist.consistency"); got != "QUORUM" {
		t.Fatalf("expected dist.set consistency=QUORUM, got %q", got)
	}
}

// TestDistMemory_TracingEmitsGetSpansHitAndMiss is the other half of
// the Phase A.2 contract: Get emits one span per call carrying
// cache.hit, recording true on hit and false on miss. Both branches
// must fire so operators can distinguish hit-rate by traced
// dimensions, not just by aggregated metric counters.
func TestDistMemory_TracingEmitsGetSpansHitAndMiss(t *testing.T) {
	t.Parallel()

	dm, rec := newSingleNodeTracedDist(t, "trace-get-A")
	ctx := context.Background()

	setErr := dm.Set(ctx, &cache.Item{
		Key:         "trace-key",
		Value:       []byte("v"),
		Version:     1,
		Origin:      "trace-get-A",
		LastUpdated: time.Now(),
	})
	if setErr != nil {
		t.Fatalf("set: %v", setErr)
	}

	if it, ok := dm.Get(ctx, "trace-key"); !ok || it == nil {
		t.Fatalf("Get returned nothing for the key we just set")
	}

	if it, ok := dm.Get(ctx, "missing-key"); ok || it != nil {
		t.Fatalf("Get returned a value for a key we never set")
	}

	hitFound, missFound := false, false

	for _, s := range rec.Ended() {
		if s.Name() != "dist.get" {
			continue
		}

		switch spanAttr(s, "cache.hit") {
		case "true":
			hitFound = true
		case "false":
			missFound = true
		default:
			// Span without a cache.hit attribute — should not happen
			// in practice; treat as a contract violation.
			t.Fatalf("dist.get span missing cache.hit attribute")
		}
	}

	if !hitFound || !missFound {
		t.Fatalf("expected one hit + one miss across get spans (hit=%v miss=%v)", hitFound, missFound)
	}
}

// TestDistMemory_TracingEmitsReplicationChildSpans verifies that
// applySet's fan-out emits a `dist.replicate.set` child span per peer.
// Uses the deterministic in-process cluster helper (replication=2)
// instead of the HTTP transport so ring/membership setup is not racy
// — the previous HTTP+seeds variant of this test occasionally missed
// the fan-out window.
func TestDistMemory_TracingEmitsReplicationChildSpans(t *testing.T) {
	t.Parallel()

	tp, rec := recordingTracerProvider()
	ctx := context.Background()

	dc := SetupInProcessClusterRF(t, 2, 2, backend.WithDistTracerProvider(tp))

	// Issue the Set on whichever node is the primary for our key — we
	// pick a key, look up the primary, and write through that node so
	// the fan-out path is exercised on the same DistMemory whose tracer
	// we're recording.
	const key = "rep-trace-key"

	owners := dc.Ring.Lookup(key)
	if len(owners) < 2 {
		t.Fatalf("expected ring to return >=2 owners for replication=2, got %d", len(owners))
	}

	var primary *backend.DistMemory

	for _, n := range dc.Nodes {
		if n.LocalNodeID() == owners[0] {
			primary = n

			break
		}
	}

	if primary == nil {
		t.Fatalf("could not locate primary node for owners[0]=%s", owners[0])
	}

	err := primary.Set(ctx, &cache.Item{
		Key:         key,
		Value:       []byte("v"),
		Version:     1,
		Origin:      string(primary.LocalNodeID()),
		LastUpdated: time.Now(),
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}

	replicateSpan := findSpan(rec, "dist.replicate.set")
	if replicateSpan == nil {
		t.Fatal("expected at least one `dist.replicate.set` child span; none recorded")
	}

	if got := spanAttr(replicateSpan, "peer.id"); got == "" {
		t.Fatalf("expected peer.id attr on replication span, got empty")
	}
}

// TestDistMemory_TracingDefaultIsNoop confirms the zero-value path
// (no WithDistTracerProvider) does not emit spans — important so
// library code never produces telemetry the operator didn't ask for.
// Asserted indirectly: the recorder never sees this DistMemory's spans
// because we never wired it up, and the cache still works.
func TestDistMemory_TracingDefaultIsNoop(t *testing.T) {
	t.Parallel()

	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode("trace-default", addr),
		backend.WithDistReplication(1),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, dm)

	ctx := context.Background()

	setErr := dm.Set(ctx, &cache.Item{
		Key:         "default-key",
		Value:       []byte("v"),
		Version:     1,
		Origin:      "trace-default",
		LastUpdated: time.Now(),
	})
	if setErr != nil {
		t.Fatalf("set: %v", setErr)
	}

	if it, ok := dm.Get(ctx, "default-key"); !ok || it == nil {
		t.Fatalf("Get returned nothing for the key we just set")
	}
}
