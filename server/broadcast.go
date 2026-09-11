package server

import (
	"sync"

	"github.com/Autumn-27/artex/db"
)

// Broadcaster is a per-task in-process pub/sub for live activity events. The
// engine publishes each appended activity at its single emit point; SSE handlers
// subscribe per task. Storage (activity table) and the live stream come from the
// same Publish call, so they never diverge.
type Broadcaster struct {
	mu   sync.Mutex
	subs map[string]map[chan db.Activity]struct{} // task id -> set of subscriber channels
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: map[string]map[chan db.Activity]struct{}{}}
}

// Subscribe returns a buffered channel of activities for a task plus an
// unsubscribe func the caller must invoke (defer) to release it.
func (b *Broadcaster) Subscribe(task string) (<-chan db.Activity, func()) {
	ch := make(chan db.Activity, 256)
	b.mu.Lock()
	if b.subs[task] == nil {
		b.subs[task] = map[chan db.Activity]struct{}{}
	}
	b.subs[task][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			if m := b.subs[task]; m != nil {
				if _, subscribed := m[ch]; subscribed {
					delete(m, ch)
					close(ch)
				}
				if len(m) == 0 {
					delete(b.subs, task)
				}
			}
			b.mu.Unlock()
		})
	}
}

// Publish fans an activity out to all subscribers of a task. Non-blocking: if a
// subscriber's buffer is full it is disconnected — the client reconnects with
// its last seq cursor and catches up the gap from the DB, so liveness never
// stalls the engine.
func (b *Broadcaster) Publish(task string, a db.Activity) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[task] {
		select {
		case ch <- a:
		default:
			// A silent drop followed by a later id would skip this event forever.
			// Drain buffered events then reconnect from the last delivered id.
			delete(b.subs[task], ch)
			close(ch)
		}
	}
	if len(b.subs[task]) == 0 {
		delete(b.subs, task)
	}
}
