package backend

import (
	"log/slog"
	"testing"
	"time"
)

// newBackoffTestDM constructs the bare-minimum DistMemory the backoff
// logic needs: a logger (the real constructor always wires one;
// updateAutoSyncBackoff dereferences it on factor change). Real
// production paths go through NewDistMemory; this fixture exists only
// so we can exercise the pure scheduling logic without a transport.
func newBackoffTestDM(maxFactor int) *DistMemory {
	return &DistMemory{
		logger:                   slog.New(slog.DiscardHandler),
		autoSyncMaxBackoffFactor: maxFactor,
	}
}

// TestAdaptiveBackoff_DisabledIsNoop confirms the default behavior is
// unchanged: with no WithDistMerkleAdaptiveBackoff option set, neither
// the factor nor the clean-tick counter ever moves regardless of tick
// outcome, and nextAutoSyncDelay always returns the raw interval. This
// is the back-compat guarantee — existing deployments see no behavioral
// shift on upgrade.
func TestAdaptiveBackoff_DisabledIsNoop(t *testing.T) {
	t.Parallel()

	dm := newBackoffTestDM(0)

	// Several clean ticks — counter and factor must stay flat.
	for range 5 {
		dm.updateAutoSyncBackoff(true)
	}

	if got := dm.autoSyncBackoffFactor.Load(); got != 0 {
		t.Errorf("backoff factor: want 0 (uninitialized, disabled), got %d", got)
	}

	if got := dm.autoSyncCleanTicks.Load(); got != 0 {
		t.Errorf("clean ticks: want 0 (disabled), got %d", got)
	}

	if got := dm.nextAutoSyncDelay(30 * time.Second); got != 30*time.Second {
		t.Errorf("nextAutoSyncDelay (disabled): want 30s, got %v", got)
	}
}

// TestAdaptiveBackoff_RampsAndCaps walks the factor through its
// doubling progression up to the configured maximum, verifies the cap
// holds across additional clean ticks, and confirms a single dirty
// tick snaps the factor back to 1.
func TestAdaptiveBackoff_RampsAndCaps(t *testing.T) {
	t.Parallel()

	dm := newBackoffTestDM(8)
	dm.autoSyncBackoffFactor.Store(1) // mirrors what the loop does at start

	// 1 -> 2 -> 4 -> 8 -> 8 (capped) -> 8 (still capped).
	want := []int64{2, 4, 8, 8, 8}
	for i, w := range want {
		dm.updateAutoSyncBackoff(true)

		got := dm.autoSyncBackoffFactor.Load()
		if got != w {
			t.Errorf("clean tick %d: want factor %d, got %d", i+1, w, got)
		}
	}

	if got := dm.autoSyncCleanTicks.Load(); got != int64(len(want)) {
		t.Errorf("clean-ticks counter: want %d, got %d", len(want), got)
	}

	// Dirty tick: factor must immediately reset to 1, counter must not
	// increment (clean-ticks only tracks clean ticks).
	dm.updateAutoSyncBackoff(false)

	if got := dm.autoSyncBackoffFactor.Load(); got != 1 {
		t.Errorf("after dirty tick: want factor 1 (reset), got %d", got)
	}

	if got := dm.autoSyncCleanTicks.Load(); got != int64(len(want)) {
		t.Errorf("clean-ticks counter must not bump on dirty tick: want %d, got %d", len(want), got)
	}

	// Ramp begins again from 1.
	dm.updateAutoSyncBackoff(true)

	if got := dm.autoSyncBackoffFactor.Load(); got != 2 {
		t.Errorf("clean tick after reset: want factor 2, got %d", got)
	}
}

// TestAdaptiveBackoff_NextDelayMultiplies confirms the timer-driven
// scheduler multiplies the base interval by the current factor. This
// is the contract the autoSyncLoop relies on to actually sleep longer
// — without this, the metric would move but the loop would still wake
// every base interval.
func TestAdaptiveBackoff_NextDelayMultiplies(t *testing.T) {
	t.Parallel()

	dm := newBackoffTestDM(16)
	dm.autoSyncBackoffFactor.Store(4)

	base := 30 * time.Second

	got := dm.nextAutoSyncDelay(base)
	if got != 4*base {
		t.Errorf("nextAutoSyncDelay at factor 4: want %v, got %v", 4*base, got)
	}

	// Factor < 1 (shouldn't happen under normal operation, but guard
	// against a future zero-store race): clamp to 1.
	dm.autoSyncBackoffFactor.Store(0)

	if got := dm.nextAutoSyncDelay(base); got != base {
		t.Errorf("nextAutoSyncDelay with factor 0 should clamp to 1×: want %v, got %v", base, got)
	}
}

// TestAdaptiveBackoff_MaxFactorOneStaysDisabled pins the edge case
// where an operator passes maxFactor=1: that's semantically "disabled"
// (no multiplication), and we treat it as such. The option helper
// already normalizes sub-1 values, but a literal 1 is still valid
// configuration and must behave like the disabled default.
func TestAdaptiveBackoff_MaxFactorOneStaysDisabled(t *testing.T) {
	t.Parallel()

	dm := newBackoffTestDM(1)
	dm.autoSyncBackoffFactor.Store(1)

	for range 5 {
		dm.updateAutoSyncBackoff(true)
	}

	if got := dm.autoSyncBackoffFactor.Load(); got != 1 {
		t.Errorf("maxFactor=1 must keep factor at 1, got %d", got)
	}

	if got := dm.autoSyncCleanTicks.Load(); got != 0 {
		t.Errorf("maxFactor=1 must not count clean ticks, got %d", got)
	}
}

// TestAdaptiveBackoff_OptionNormalizesNegatives covers the option
// helper itself: negative or zero values are coerced to 0 (the
// "disabled" sentinel), preventing accidental enablement with a
// surprising factor.
func TestAdaptiveBackoff_OptionNormalizesNegatives(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   int
		want int
	}{
		{in: -5, want: 0},
		{in: 0, want: 0},
		{in: 1, want: 1},
		{in: 8, want: 8},
	}

	for _, tc := range cases {
		dm := newBackoffTestDM(0)
		WithDistMerkleAdaptiveBackoff(tc.in)(dm)

		if dm.autoSyncMaxBackoffFactor != tc.want {
			t.Errorf("WithDistMerkleAdaptiveBackoff(%d): want %d, got %d", tc.in, tc.want, dm.autoSyncMaxBackoffFactor)
		}
	}
}
