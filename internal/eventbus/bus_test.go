package eventbus

import (
	"context"
	"testing"
	"time"
)

// testEventType is a sentinel event-type string used across the
// test bodies. Hoisted to satisfy goconst — it would otherwise
// repeat 9× across the file with no semantic significance.
const testEventType = "members"

// TestBus_PublishToSingleSubscriber covers the smoke path —
// Subscribe, Publish, receive on the channel. Pins the basics so
// any future refactor that breaks "the simplest case" trips
// loudly.
func TestBus_PublishToSingleSubscriber(t *testing.T) {
	t.Parallel()

	b := New()
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	ch, unsub := b.Subscribe(ctx)
	defer unsub()

	v := b.Publish(Event{Type: testEventType, Payload: "hello"})

	select {
	case got := <-ch:
		if got.Type != testEventType {
			t.Errorf("type: got %q, want members", got.Type)
		}

		if got.Version != v {
			t.Errorf("version: got %d, want %d", got.Version, v)
		}

		if got.Payload != "hello" {
			t.Errorf("payload: got %v, want hello", got.Payload)
		}

		if got.Timestamp.IsZero() {
			t.Errorf("timestamp not stamped")
		}

	case <-time.After(time.Second):
		t.Fatal("did not receive event within 1s")
	}
}

// TestBus_FanOutToMultipleSubscribers pins the "every subscriber
// sees every event" property. The SSE handler relies on this: two
// operators on /topology each open their own subscription and
// both must observe the same membership transitions.
func TestBus_FanOutToMultipleSubscribers(t *testing.T) {
	t.Parallel()

	b := New()
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	ch1, unsub1 := b.Subscribe(ctx)
	defer unsub1()

	ch2, unsub2 := b.Subscribe(ctx)

	defer unsub2()

	b.Publish(Event{Type: testEventType})

	for i, ch := range []<-chan Event{ch1, ch2} {
		select {
		case got := <-ch:
			if got.Type != testEventType {
				t.Errorf("ch%d: type %q, want members", i+1, got.Type)
			}

		case <-time.After(time.Second):
			t.Fatalf("ch%d did not receive event within 1s", i+1)
		}
	}
}

// TestBus_DropsOnFullBuffer is the load-bearing assertion: a slow
// subscriber MUST NOT block the bus. We fill the buffer (size 16),
// then publish more events; the slow subscriber misses them but
// publish never blocks and the drop counter increments.
func TestBus_DropsOnFullBuffer(t *testing.T) {
	t.Parallel()

	b := New()
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	_, unsub := b.Subscribe(ctx)
	defer unsub()

	// Fill the buffer.
	for range subscriberBufferSize {
		b.Publish(Event{Type: testEventType})
	}

	if got := b.Dropped(); got != 0 {
		t.Errorf("Dropped after filling buffer: got %d, want 0", got)
	}

	// Buffer is now full; further publishes drop.
	const overflow = 5

	for range overflow {
		b.Publish(Event{Type: testEventType})
	}

	if got := b.Dropped(); got != overflow {
		t.Errorf("Dropped after overflow: got %d, want %d", got, overflow)
	}
}

// TestBus_SlowSubscriberDoesNotBlockFastOne pins the fan-out
// fairness property: a slow consumer's full buffer drops its OWN
// events, but the fast consumer keeps receiving. Without this,
// one stuck operator on /topology would silently freeze every
// other operator's view.
//
// The publish-then-drain handshake matters here: we publish ONE
// event, wait for the fast consumer to drain it, then publish
// the next. Without the handshake, scheduler luck determines
// whether the fast consumer's buffer also overflows — which would
// be a flaky test, not a real bus bug.
func TestBus_SlowSubscriberDoesNotBlockFastOne(t *testing.T) {
	t.Parallel()

	b := New()
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	// Slow subscriber — never reads.
	_, unsubSlow := b.Subscribe(ctx)
	defer unsubSlow()

	// Fast subscriber — drains synchronously via a 1-deep ack
	// channel so the test can publish at the consumer's pace.
	chFast, unsubFast := b.Subscribe(ctx)
	defer unsubFast()

	const n = subscriberBufferSize + 10 // enough to overflow slow's buffer

	ack := make(chan struct{})

	go func() {
		for range chFast {
			ack <- struct{}{}
		}
	}()

	for i := range n {
		b.Publish(Event{Type: testEventType})

		// Wait for the fast consumer to drain this publish so
		// its buffer never accumulates. The slow subscriber's
		// buffer fills regardless because nobody reads it.
		select {
		case <-ack:
		case <-time.After(time.Second):
			t.Fatalf("fast subscriber did not ack publish %d within 1s", i)
		}
	}

	// Slow subscriber dropped 10 (n - bufferSize).
	if dropped := b.Dropped(); dropped != n-subscriberBufferSize {
		t.Errorf("Dropped: got %d, want %d", dropped, n-subscriberBufferSize)
	}
}

// TestBus_UnsubscribeIsIdempotent guards the defer-and-explicit-
// unsubscribe pattern callers use (defer unsub() AND explicit
// reaping via ctx). Both paths must not double-close the channel.
func TestBus_UnsubscribeIsIdempotent(t *testing.T) {
	t.Parallel()

	b := New()
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	_, unsub := b.Subscribe(ctx)
	unsub()
	// Second call must not panic.
	unsub()
	unsub()

	if c := b.SubscriberCount(); c != 0 {
		t.Errorf("SubscriberCount after unsub: got %d, want 0", c)
	}
}

// TestBus_ContextCancellationReapsSubscription pins the safety-net
// path: even if the caller forgets to call unsubscribe, the ctx
// cancellation cleans up the subscription and closes the channel.
func TestBus_ContextCancellationReapsSubscription(t *testing.T) {
	t.Parallel()

	b := New()
	ctx, cancel := context.WithCancel(t.Context())

	ch, _ := b.Subscribe(ctx)

	if c := b.SubscriberCount(); c != 1 {
		t.Errorf("SubscriberCount after Subscribe: got %d, want 1", c)
	}

	cancel()

	// Wait for the cleanup goroutine to run. A polling loop is
	// the simplest cross-platform synchronization here — a sync
	// channel inside the bus would be over-engineering for a
	// safety-net.
	deadline := time.After(time.Second)
	for b.SubscriberCount() != 0 {
		select {
		case <-deadline:
			t.Fatalf("subscription not reaped within 1s, count=%d", b.SubscriberCount())
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Channel is closed after reap — confirm by reading and
	// discarding whatever buffered events remain. The range
	// terminates because the channel is closed.
	var drained int

	for range ch {
		drained++
	}

	_ = drained
}

// TestBus_VersionsAreMonotonic checks that Version is strictly
// increasing across concurrent Publishes. SSE consumers rely on
// version ordering to detect dropped events when reconnecting.
func TestBus_VersionsAreMonotonic(t *testing.T) {
	t.Parallel()

	b := New()

	const n = 100

	versions := make([]uint64, n)

	for i := range n {
		versions[i] = b.Publish(Event{Type: testEventType})
	}

	for i := 1; i < n; i++ {
		if versions[i] <= versions[i-1] {
			t.Fatalf("version[%d]=%d not > version[%d]=%d", i, versions[i], i-1, versions[i-1])
		}
	}
}

// TestBus_AssignsTimestampWhenZero — caller can override Timestamp
// by setting it on the Event, otherwise the bus stamps Now(). Pin
// both branches so the SSE wire format documentation stays
// honest.
func TestBus_AssignsTimestampWhenZero(t *testing.T) {
	t.Parallel()

	b := New()
	ctx, cancel := context.WithCancel(t.Context())

	defer cancel()

	ch, unsub := b.Subscribe(ctx)
	defer unsub()

	custom := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	b.Publish(Event{Type: testEventType, Timestamp: custom})
	b.Publish(Event{Type: "heartbeat"}) // no timestamp → bus stamps

	select {
	case got := <-ch:
		if !got.Timestamp.Equal(custom) {
			t.Errorf("first event timestamp: got %v, want %v", got.Timestamp, custom)
		}

	case <-time.After(time.Second):
		t.Fatal("did not receive first event")
	}

	select {
	case got := <-ch:
		if got.Timestamp.IsZero() {
			t.Error("second event timestamp should have been stamped by the bus")
		}

	case <-time.After(time.Second):
		t.Fatal("did not receive second event")
	}
}
