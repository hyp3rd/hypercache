package tests

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/hyp3rd/hypercache/pkg/backend"
)

// captureStore is the shared backing storage for captureHandler clones
// produced via WithAttrs / WithGroup. slog calls WithAttrs to install
// the component/node_id attrs bound in NewDistMemory; that returned
// clone still needs to write into the original test's records slice,
// not its own. Sharing the store via a pointer is the simplest way to
// keep that invariant.
type captureStore struct {
	mu      sync.Mutex
	records []slog.Record
}

// captureHandler is a minimal slog.Handler that records every emitted
// record into a shared captureStore. Used to assert that DistMemory
// emits the expected structured-log events without text scraping.
type captureHandler struct {
	store *captureStore
	attrs []slog.Attr
}

func newCaptureHandler() *captureHandler { return &captureHandler{store: &captureStore{}} }

func (*captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	// Re-attach the WithAttrs-prefixed attrs onto the record so the
	// caller can assert against them — slog routes With(...) calls
	// through WithAttrs and those attrs would otherwise live only on
	// the cloned handler, not the captured Record.
	cloned := r.Clone()
	for _, a := range h.attrs {
		cloned.AddAttrs(a)
	}

	h.store.mu.Lock()
	defer h.store.mu.Unlock()

	h.store.records = append(h.store.records, cloned)

	return nil
}

func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))

	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)

	return &captureHandler{store: h.store, attrs: merged}
}

func (h *captureHandler) WithGroup(_ string) slog.Handler { return h }

// snapshot returns a copy of the recorded events under lock.
func (h *captureHandler) snapshot() []slog.Record {
	h.store.mu.Lock()
	defer h.store.mu.Unlock()

	out := make([]slog.Record, len(h.store.records))
	copy(out, h.store.records)

	return out
}

// recordHasAttr is a small helper for the asserts below. slog.Record's
// Attrs is iterator-style (closure), so a one-line check needs a helper.
func recordHasAttr(r slog.Record, key, want string) bool {
	found := false

	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key && a.Value.String() == want {
			found = true

			return false
		}

		return true
	})

	return found
}

// TestDistMemory_LoggerEmitsListenerStart is the contract test for Phase
// A.1 logging: when a caller supplies a logger via WithDistLogger, the
// dist backend must emit a structured `dist HTTP listener started`
// record carrying the node identity bound at construction.
//
// Picking the listener-start event is deliberate — it fires on every
// successful node start, so the test is deterministic. Other log sites
// (peer suspect, hint dropped, migration failure) take the same plumbing
// and would only test the same wiring twice.
func TestDistMemory_LoggerEmitsListenerStart(t *testing.T) {
	t.Parallel()

	addr := AllocatePort(t)
	handler := newCaptureHandler()
	logger := slog.New(handler)

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode("log-test-A", addr),
		backend.WithDistReplication(1),
		backend.WithDistLogger(logger),
	)
	if err != nil {
		t.Fatalf("new dist memory: %v", err)
	}

	dm, ok := bi.(*backend.DistMemory)
	if !ok {
		t.Fatalf("expected *backend.DistMemory, got %T", bi)
	}

	StopOnCleanup(t, dm)

	records := handler.snapshot()

	var startRec *slog.Record

	for i := range records {
		if records[i].Message == "dist HTTP listener started" {
			startRec = &records[i]

			break
		}
	}

	if startRec == nil {
		t.Fatalf("expected `dist HTTP listener started` record; got %d records", len(records))
	}

	if startRec.Level != slog.LevelInfo {
		t.Fatalf("expected Info level, got %v", startRec.Level)
	}

	// node_id and component are bound at construction via .With(...) —
	// recordHasAttr checks the per-record attrs, but slog routes the
	// With-bound attrs through WithAttrs on the handler. The capture
	// handler re-attaches them in Handle so the assertion below is
	// against the merged set.
	if !recordHasAttr(*startRec, "node_id", "log-test-A") {
		t.Fatalf("expected node_id=log-test-A attr on record, got attrs missing it")
	}

	if !recordHasAttr(*startRec, "component", "dist_memory") {
		t.Fatalf("expected component=dist_memory attr on record")
	}

	if !recordHasAttr(*startRec, "addr", addr) {
		t.Fatalf("expected addr=%s attr on record", addr)
	}
}

// TestDistMemory_LoggerDefaultIsSilent confirms the zero-value path
// (no WithDistLogger) does not write anywhere — important because
// library code must not pollute the application's stderr by default.
// We assert this indirectly by constructing without a logger and
// verifying construction succeeds (the discard handler is a no-op).
func TestDistMemory_LoggerDefaultIsSilent(t *testing.T) {
	t.Parallel()

	addr := AllocatePort(t)

	bi, err := backend.NewDistMemory(
		context.Background(),
		backend.WithDistNode("log-default", addr),
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
}
