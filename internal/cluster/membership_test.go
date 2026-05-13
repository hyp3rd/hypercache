package cluster

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestMembership_OnStateChange_FiresAfterMutation pins the
// "observers run AFTER the lock is released and the mutation is
// committed" contract. An observer reading via List() must see
// the new state, not the pre-mutation state — verifying we don't
// invoke observers from inside the mutex.
func TestMembership_OnStateChange_FiresAfterMutation(t *testing.T) {
	t.Parallel()

	m := NewMembership(NewRing())
	m.Upsert(NewNode("n1", "127.0.0.1:7946"))

	var (
		seenState   NodeState
		seenVersion uint64
		seenList    []*Node
	)

	m.OnStateChange(func(_ NodeID, state NodeState, version uint64) {
		seenState = state
		seenVersion = version
		// List() takes the membership read-lock. If observers
		// were invoked from inside the write lock, this would
		// deadlock. The test passing means the lock IS released
		// before observers fire.
		seenList = m.List()
	})

	m.Mark("n1", NodeSuspect)

	if seenState != NodeSuspect {
		t.Errorf("observer state: got %v, want NodeSuspect", seenState)
	}

	if seenVersion == 0 {
		t.Errorf("observer version: got 0, want > 0")
	}

	if len(seenList) != 1 || seenList[0].State != NodeSuspect {
		t.Errorf("observer list reflects pre-mutation state; got %+v", seenList)
	}
}

// TestMembership_OnStateChange_FiresOnUpsertAndRemove covers the
// non-Mark mutation paths. Upsert is the gossip-driven
// "node-discovered" path; Remove is the "post-deadAfter prune"
// path. Both must reach observers so an SSE consumer sees the
// full lifecycle, not just suspect/alive transitions.
func TestMembership_OnStateChange_FiresOnUpsertAndRemove(t *testing.T) {
	t.Parallel()

	m := NewMembership(NewRing())

	type seen struct {
		id      NodeID
		state   NodeState
		version uint64
	}

	var events []seen

	m.OnStateChange(func(id NodeID, state NodeState, version uint64) {
		events = append(events, seen{id, state, version})
	})

	m.Upsert(NewNode("n1", "127.0.0.1:7946"))
	m.Mark("n1", NodeSuspect)
	m.Remove("n1")

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 (upsert+mark+remove)", len(events))
	}

	wantStates := []NodeState{NodeAlive, NodeSuspect, NodeDead}
	for i, e := range events {
		if e.id != "n1" {
			t.Errorf("event %d: id %q, want n1", i, e.id)
		}

		if e.state != wantStates[i] {
			t.Errorf("event %d: state %v, want %v", i, e.state, wantStates[i])
		}

		if i > 0 && e.version <= events[i-1].version {
			t.Errorf("event %d: version %d not > previous %d", i, e.version, events[i-1].version)
		}
	}
}

// TestMembership_OnStateChange_NoEventOnNoOpMark guards against
// emitting events for mutations that didn't actually mutate.
// Marking a non-existent node returns false and must NOT fire
// observers — an SSE consumer would otherwise see ghost
// "transitioned to suspect" events for nodes that never existed.
func TestMembership_OnStateChange_NoEventOnNoOpMark(t *testing.T) {
	t.Parallel()

	m := NewMembership(NewRing())

	var fired atomic.Int32

	m.OnStateChange(func(_ NodeID, _ NodeState, _ uint64) {
		fired.Add(1)
	})

	if m.Mark("ghost", NodeSuspect) {
		t.Fatal("Mark on non-existent node returned true")
	}

	if got := fired.Load(); got != 0 {
		t.Errorf("observer fired %d times for no-op Mark, want 0", got)
	}

	// Same shape for a no-op Remove.
	if m.Remove("ghost") {
		t.Fatal("Remove on non-existent node returned true")
	}

	if got := fired.Load(); got != 0 {
		t.Errorf("observer fired %d times for no-op Remove, want 0", got)
	}
}

// TestMembership_OnStateChange_MultipleObservers covers the
// fan-out path — multiple registered observers all see every
// mutation, in registration order. The cache binary registers
// (1) the SSE event-bus publisher and (2) potentially metrics
// counters; both need to fire on every event.
func TestMembership_OnStateChange_MultipleObservers(t *testing.T) {
	t.Parallel()

	m := NewMembership(NewRing())
	m.Upsert(NewNode("n1", "127.0.0.1:7946"))

	var firstCalls, secondCalls int32

	m.OnStateChange(func(_ NodeID, _ NodeState, _ uint64) {
		atomic.AddInt32(&firstCalls, 1)
	})
	m.OnStateChange(func(_ NodeID, _ NodeState, _ uint64) {
		atomic.AddInt32(&secondCalls, 1)
	})

	m.Mark("n1", NodeSuspect)
	m.Mark("n1", NodeAlive)

	if got := atomic.LoadInt32(&firstCalls); got != 2 {
		t.Errorf("first observer: %d calls, want 2", got)
	}

	if got := atomic.LoadInt32(&secondCalls); got != 2 {
		t.Errorf("second observer: %d calls, want 2", got)
	}
}

// TestMembership_OnStateChange_DoesNotBlockMutation pins the
// "slow observer doesn't backpressure the heartbeat loop" claim.
// Mutation must complete (and Version() must reflect the new
// value) before the observer's blocking work finishes.
//
// The Membership package itself doesn't promise the observer is
// async — but it DOES promise the lock is released before the
// observer runs. So a different goroutine acquiring the lock
// during the slow observer must succeed without waiting on the
// observer.
func TestMembership_OnStateChange_DoesNotBlockMutation(t *testing.T) {
	t.Parallel()

	m := NewMembership(NewRing())
	m.Upsert(NewNode("n1", "127.0.0.1:7946"))

	releaseObserver := make(chan struct{})
	observerEntered := make(chan struct{})
	// Observer fires on every mutation; only the FIRST invocation
	// should block. Subsequent invocations (e.g. the concurrent
	// Upsert below) must return promptly so the test's own
	// orchestration mutations don't deadlock.
	var fired atomic.Int32

	m.OnStateChange(func(_ NodeID, _ NodeState, _ uint64) {
		if fired.Add(1) == 1 {
			close(observerEntered)
			<-releaseObserver
		}
	})

	var wg sync.WaitGroup

	wg.Go(func() {
		m.Mark("n1", NodeSuspect)
	})

	<-observerEntered // observer is now stuck on the Mark call

	// While the observer blocks, another mutation should still
	// be able to take the lock — proving the lock isn't held
	// during observer invocation.
	m.Upsert(NewNode("n2", "127.0.0.1:7947"))

	if len(m.List()) != 2 {
		t.Errorf("expected 2 nodes after concurrent Upsert, got %d", len(m.List()))
	}

	close(releaseObserver)
	wg.Wait()
}

// TestMembership_Mark_NoBumpOnSameState pins the
// transition-only-bump contract: Mark() must NOT increment a node's
// incarnation when the requested state matches the current state.
// Without this rule, steady-state heartbeat success paths
// (evaluateLiveness → Mark(peer, Alive)) inflate the counter once
// per probe — operators saw incarnation values in the thousands
// after a few hours of normal operation. Incarnation is owned by
// the node itself in SWIM; observers should not churn it.
func TestMembership_Mark_NoBumpOnSameState(t *testing.T) {
	t.Parallel()

	m := NewMembership(NewRing())
	m.Upsert(NewNode("n1", "127.0.0.1:7946"))

	before := m.List()[0].Incarnation

	// Repeat the same Mark a few times — this models the
	// "successful probe every second" pattern that drove the bug.
	for range 50 {
		m.Mark("n1", NodeAlive)
	}

	after := m.List()[0].Incarnation
	if after != before {
		t.Errorf("incarnation churned on same-state Mark: before=%d after=%d (want stable)",
			before, after)
	}
}

// TestMembership_Mark_SameStateIsFullNoOp pins the rest of the
// no-op-Mark contract beyond just incarnation: when state matches
// the current value, the membership version vector must NOT
// advance and registered observers must NOT fire. Without this
// rule, every successful heartbeat probe bumps the version
// counter once per peer per interval — a 5-node cluster running
// for a few hours showed MembershipVersion past 4,800 even though
// no nodes had actually changed state. Cascading effects: gossip
// fans out spurious "version went up" deltas, SSE consumers see
// constant "members" event spam, and the metric stops being
// useful as a real-membership-change indicator.
func TestMembership_Mark_SameStateIsFullNoOp(t *testing.T) {
	t.Parallel()

	m := NewMembership(NewRing())
	m.Upsert(NewNode("n1", "127.0.0.1:7946"))

	var fired atomic.Int32

	m.OnStateChange(func(_ NodeID, _ NodeState, _ uint64) {
		fired.Add(1)
	})

	// Capture the version baseline after the Upsert above.
	versionBefore := m.Version()

	// Pound on Mark with the existing state. Models the steady
	// "probe succeeded again" pattern that drove the bug.
	for range 100 {
		m.Mark("n1", NodeAlive)
	}

	if got := m.Version(); got != versionBefore {
		t.Errorf("Version drifted on same-state Mark: before=%d after=%d (want stable)",
			versionBefore, got)
	}

	if got := fired.Load(); got != 0 {
		t.Errorf("observer fired %d times for same-state Mark, want 0", got)
	}

	// Sanity: LastSeen still refreshes so the suspect-timeout
	// machinery sees probes. We can't assert exact wall-clock
	// values cleanly here, but we can assert it advanced past the
	// Upsert moment by being non-zero.
	if m.List()[0].LastSeen.IsZero() {
		t.Errorf("LastSeen wasn't refreshed by same-state Mark")
	}
}

// TestMembership_Mark_BumpsOnTransition guards the other side of the
// contract: a genuine state transition (Alive→Suspect, Suspect→Alive,
// etc.) MUST bump incarnation so the gossip-merge rule
// "higher incarnation wins" propagates the change cluster-wide. If we
// over-suppress, transitions would silently fail to propagate and a
// peer briefly marked Suspect would stay Suspect on neighbouring
// nodes forever.
func TestMembership_Mark_BumpsOnTransition(t *testing.T) {
	t.Parallel()

	m := NewMembership(NewRing())
	m.Upsert(NewNode("n1", "127.0.0.1:7946"))

	v0 := m.List()[0].Incarnation

	m.Mark("n1", NodeSuspect)

	v1 := m.List()[0].Incarnation
	if v1 != v0+1 {
		t.Errorf("Alive→Suspect: got incarnation %d, want %d", v1, v0+1)
	}

	m.Mark("n1", NodeAlive)

	v2 := m.List()[0].Incarnation
	if v2 != v1+1 {
		t.Errorf("Suspect→Alive: got incarnation %d, want %d", v2, v1+1)
	}

	// Same-state again — must NOT bump even after recent transitions.
	m.Mark("n1", NodeAlive)
	m.Mark("n1", NodeAlive)

	v3 := m.List()[0].Incarnation
	if v3 != v2 {
		t.Errorf("Alive→Alive after a transition burst: got %d, want stable at %d", v3, v2)
	}
}

// TestMembership_Refute_AlwaysBumps pins the SWIM self-refute
// primitive: Refute() unconditionally increments incarnation, even
// when the local view of the node is already Alive. Without this,
// the refutation packet a node sends back to a peer that suspected
// it would carry the SAME incarnation as the suspect claim — and
// "higher incarnation wins" would refuse to overwrite, so the
// suspect claim would stick even though the node refuted it.
func TestMembership_Refute_AlwaysBumps(t *testing.T) {
	t.Parallel()

	m := NewMembership(NewRing())
	m.Upsert(NewNode("self", "127.0.0.1:7946"))

	v0 := m.List()[0].Incarnation

	// Local state is Alive — a transition-only rule would no-op
	// here, which would silently break refutation propagation.
	m.Refute("self")

	v1 := m.List()[0].Incarnation
	if v1 != v0+1 {
		t.Errorf("first Refute: got incarnation %d, want %d", v1, v0+1)
	}

	// Each subsequent refute climbs one more — chained suspect
	// claims from different peers must each be answerable with a
	// strictly-higher incarnation.
	m.Refute("self")
	m.Refute("self")

	v2 := m.List()[0].Incarnation
	if v2 != v1+2 {
		t.Errorf("chained Refute: got incarnation %d, want %d", v2, v1+2)
	}

	// State must end Alive regardless of intermediate values.
	if got := m.List()[0].State; got != NodeAlive {
		t.Errorf("Refute state: got %v, want NodeAlive", got)
	}
}

// TestMembership_Refute_GhostReturnsFalse mirrors the
// non-existent-node guard already present on Mark/Remove.
func TestMembership_Refute_GhostReturnsFalse(t *testing.T) {
	t.Parallel()

	m := NewMembership(NewRing())

	var fired atomic.Int32

	m.OnStateChange(func(_ NodeID, _ NodeState, _ uint64) {
		fired.Add(1)
	})

	if m.Refute("ghost") {
		t.Fatal("Refute on non-existent node returned true")
	}

	if got := fired.Load(); got != 0 {
		t.Errorf("observer fired %d times for ghost Refute, want 0", got)
	}
}
