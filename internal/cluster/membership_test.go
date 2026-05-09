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
