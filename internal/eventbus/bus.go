// Package eventbus is a small in-process fan-out broadcaster used
// by the management HTTP server's SSE endpoint to push topology
// updates to every connected operator.
//
// Design points:
//
//   - **Drop-on-full publish.** Each subscriber owns a small
//     buffered channel. If a subscriber is slow and its buffer
//     fills, Publish DROPS the event for that subscriber rather
//     than blocking — the SWIM heartbeat loop publishes events
//     and must never wait on an unresponsive consumer. Subscribers
//     can detect drops via the Dropped() counter and reconnect to
//     get a fresh snapshot.
//
//   - **Context-driven unsubscribe.** Subscribe takes a context;
//     when the context cancels, the bus reaps the subscription
//     and closes the channel. Callers MUST drain the channel after
//     ctx cancellation to avoid leaking goroutines blocked on
//     send (the bus uses non-blocking send, but a half-buffered
//     channel still has buffered values).
//
//   - **No external deps.** Pure stdlib (sync, sync/atomic).
//     Testable in isolation.
//
// Out of scope: persistence, replay, fan-in, cross-process
// distribution. This is the simplest thing that works for the
// monitor's SSE consumer.
package eventbus

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Event is the payload carried over the bus. Type names the event
// shape ("members" or "heartbeat" today; new types are additive),
// Payload is the wire JSON the SSE handler will marshal, and
// Version is the bus-monotonic ordering so subscribers can detect
// gaps caused by drops.
type Event struct {
	Type      string
	Payload   any
	Version   uint64
	Timestamp time.Time
}

// subscriberBufferSize bounds each subscriber's queue. Larger →
// more memory per slow consumer; smaller → more drops on bursty
// emit. 16 is generous for SWIM-driven event rates (a few per
// second at most for a 100-node cluster).
const subscriberBufferSize = 16

// Bus is a fan-out broadcaster. The zero value is NOT usable —
// always construct via New().
type Bus struct {
	mu          sync.RWMutex
	subscribers map[uint64]*subscriber
	nextID      uint64
	version     atomic.Uint64
	dropped     atomic.Uint64
}

type subscriber struct {
	ch chan Event
}

// New constructs an empty Bus.
func New() *Bus {
	return &Bus{subscribers: make(map[uint64]*subscriber)}
}

// Subscribe registers a new subscriber. Returns a receive-only
// channel of events and an Unsubscribe function. The subscription
// is also reaped automatically when ctx is canceled (typical
// shape: caller derives ctx from the request context, the bus
// cleans up when the request ends).
//
// Callers SHOULD call the returned unsubscribe function in a
// defer; ctx-driven reaping is the safety net for when the
// caller's defer runs late or panics. Calling unsubscribe
// twice is safe (idempotent).
func (b *Bus) Subscribe(ctx context.Context) (<-chan Event, func()) {
	b.mu.Lock()

	id := b.nextID
	b.nextID++

	sub := &subscriber{ch: make(chan Event, subscriberBufferSize)}

	b.subscribers[id] = sub
	b.mu.Unlock()

	var once sync.Once

	unsub := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, id)
			b.mu.Unlock()
			close(sub.ch)
		})
	}

	// Goroutine that watches ctx; runs until either ctx cancels
	// (then we unsub) or someone else has already unsubbed (then
	// the goroutine exits via the closed channel write inside
	// once.Do — actually, ctx.Done() being a channel means we
	// just block until cancellation; the once.Do guards the
	// double-unsub case).
	go func() {
		<-ctx.Done()
		unsub()
	}()

	return sub.ch, unsub
}

// Publish broadcasts evt to every current subscriber. The publish
// is fire-and-forget: if a subscriber's buffer is full, the event
// is dropped for that subscriber and the global drop counter
// increments. Returns the version assigned to the event (useful
// for tests pinning ordering).
//
// Publish does NOT block on slow subscribers — by design the
// SWIM heartbeat goroutine drives publishes and must keep ticking
// at its configured interval regardless of consumer health.
func (b *Bus) Publish(evt Event) uint64 {
	b.mu.RLock()
	defer b.mu.RUnlock()

	// Stamp the event under the read lock so version is
	// monotonic across concurrent publishers.
	version := b.version.Add(1)

	evt.Version = version

	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now()
	}

	for _, sub := range b.subscribers {
		select {
		case sub.ch <- evt:
		default:
			b.dropped.Add(1)
		}
	}

	return version
}

// Dropped returns the cumulative count of events dropped because a
// subscriber's buffer was full. Used for telemetry / sanity checks
// in tests; not part of the SSE wire format.
func (b *Bus) Dropped() uint64 {
	return b.dropped.Load()
}

// SubscriberCount returns the number of currently registered
// subscribers. Useful for tests asserting clean unsubscribe
// behavior on context cancellation.
func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	return len(b.subscribers)
}
