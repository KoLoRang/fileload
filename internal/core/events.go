package core

import (
	"sync"
	"time"
)

// Publisher receives core notifications. The UI layer implements it (Wails
// events); tests use a recorder. Calls may come from any goroutine.
type Publisher interface {
	// Changed signals that Snapshot() would return something new.
	Changed()
	// Activity delivers a batch of file-sync messages.
	Activity(msgs []string)
}

type nopPublisher struct{}

func (nopPublisher) Changed()          {}
func (nopPublisher) Activity([]string) {}

// batcher coalesces activity messages: FUSE threads may report hundreds of
// transfers per second, the UI gets at most one event per interval.
type batcher struct {
	every time.Duration
	flush func([]string)

	mu    sync.Mutex
	buf   []string
	timer *time.Timer
}

const maxBatch = 200

func (b *batcher) add(msg string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) < maxBatch {
		b.buf = append(b.buf, msg)
	}
	if b.timer == nil {
		b.timer = time.AfterFunc(b.every, b.fire)
	}
}

func (b *batcher) fire() {
	b.mu.Lock()
	out := b.buf
	b.buf, b.timer = nil, nil
	b.mu.Unlock()
	if len(out) > 0 {
		b.flush(out)
	}
}
