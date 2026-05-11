package hypercache_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hyp3rd/hypercache"
	"github.com/hyp3rd/hypercache/internal/constants"
	"github.com/hyp3rd/hypercache/pkg/backend"
)

// TestLogging_EvictionLoopAnnouncesStart pins the structured-log
// contract operators rely on: when the eviction loop is configured
// (evictionInterval > 0), starting the cache emits an "eviction loop
// starting" record with the interval, max_per_tick, and algorithm
// fields. Without this, a silent cache failed silently — the symptom
// the WithLogger work was added to fix.
func TestLogging_EvictionLoopAnnouncesStart(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg, err := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	cfg.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithEvictionInterval[backend.InMemory](50 * time.Millisecond),
		hypercache.WithEvictionAlgorithm[backend.InMemory]("lru"),
		hypercache.WithLogger[backend.InMemory](logger),
	}
	cfg.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](10),
	}

	hc, err := hypercache.New(context.Background(), hypercache.GetDefaultManager(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(func() { _ = hc.Stop(context.Background()) })

	// Loop startup log is emitted synchronously from startEvictionRoutine,
	// so we don't need to wait for a tick.
	rec := firstRecordMatching(t, buf, "eviction loop starting")
	// slog's JSON handler encodes time.Duration as nanoseconds (float64).
	wantNs := float64((50 * time.Millisecond).Nanoseconds())

	got, ok := rec["interval"].(float64)
	if !ok {
		t.Errorf("interval: missing or wrong type, got %v (%T)", rec["interval"], rec["interval"])
	} else if got != wantNs {
		t.Errorf("interval: want %v ns, got %v", wantNs, got)
	}

	if rec["algorithm"] != "lru" {
		t.Errorf("algorithm: want lru, got %v", rec["algorithm"])
	}
}

// TestLogging_ExpirationLoopAnnouncesStart mirrors the eviction
// assertion for the expiration loop. Same operational contract,
// same regression guard.
func TestLogging_ExpirationLoopAnnouncesStart(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cfg, err := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	cfg.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithExpirationInterval[backend.InMemory](75 * time.Millisecond),
		hypercache.WithLogger[backend.InMemory](logger),
	}
	cfg.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](10),
	}

	hc, err := hypercache.New(context.Background(), hypercache.GetDefaultManager(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(func() { _ = hc.Stop(context.Background()) })

	rec := firstRecordMatching(t, buf, "expiration loop starting")
	wantNs := float64((75 * time.Millisecond).Nanoseconds())

	got, ok := rec["interval"].(float64)
	if !ok {
		t.Errorf("interval: missing or wrong type, got %v (%T)", rec["interval"], rec["interval"])
	} else if got != wantNs {
		t.Errorf("interval: want %v ns, got %v", wantNs, got)
	}
}

// TestLogging_NilLoggerResetsToDiscard documents the WithLogger(nil)
// contract: passing nil resets to the default discard handler instead
// of panicking later. Operators may unset logging at runtime via the
// `nil` shape; the cache should keep working silently.
func TestLogging_NilLoggerResetsToDiscard(t *testing.T) {
	t.Parallel()

	cfg, err := hypercache.NewConfig[backend.InMemory](constants.InMemoryBackend)
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}

	cfg.HyperCacheOptions = []hypercache.Option[backend.InMemory]{
		hypercache.WithLogger[backend.InMemory](nil),
	}
	cfg.InMemoryOptions = []backend.Option[backend.InMemory]{
		backend.WithCapacity[backend.InMemory](10),
	}

	hc, err := hypercache.New(context.Background(), hypercache.GetDefaultManager(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(func() { _ = hc.Stop(context.Background()) })

	// If WithLogger(nil) had been wired as an unguarded assignment, the
	// next operation would NPE on logger.Info. The fact that we reach
	// this line and the cache responds is the assertion.
	err = hc.Set(context.Background(), "k", "v", time.Minute)
	if err != nil {
		t.Fatalf("Set with nil logger should succeed; got: %v", err)
	}
}

// firstRecordMatching scans the JSON log stream for the first record
// whose `msg` matches the prefix, returning it as a generic map. Fails
// the test fatally if no such record exists — the assertion is "this
// log line MUST appear", not "may appear".
func firstRecordMatching(t *testing.T, buf *bytes.Buffer, msgPrefix string) map[string]any {
	t.Helper()

	for line := range strings.SplitSeq(buf.String(), "\n") {
		if line == "" {
			continue
		}

		var rec map[string]any

		err := json.Unmarshal([]byte(line), &rec)
		if err != nil {
			t.Fatalf("malformed log line: %q (%v)", line, err)
		}

		msg, ok := rec["msg"].(string)
		if ok && strings.HasPrefix(msg, msgPrefix) {
			return rec
		}
	}

	t.Fatalf("no log record with msg prefix %q in:\n%s", msgPrefix, buf.String())

	return nil
}
