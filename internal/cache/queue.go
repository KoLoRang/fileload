package cache

import (
	"context"
	"errors"
	"sync"
)

// ErrClosed is returned when work is scheduled after Close.
var ErrClosed = errors.New("cache closed")

// queue runs one job per key with a fixed number of workers.
//
//   - a key scheduled while it is already pending is coalesced;
//   - a key scheduled while it is running is re-run once afterwards, so the
//     last write always gets uploaded;
//   - the same key never runs concurrently.
type queue struct {
	run func(ctx context.Context, key string)

	mu      sync.Mutex
	cond    *sync.Cond
	order   []string        // pending keys, FIFO
	pending map[string]bool // keys in order
	running map[string]bool
	again   map[string]bool // re-run after the current run
	closed  bool
	wg      sync.WaitGroup
}

func newQueue(workers int, run func(ctx context.Context, key string)) *queue {
	q := &queue{
		run:     run,
		pending: map[string]bool{},
		running: map[string]bool{},
		again:   map[string]bool{},
	}
	q.cond = sync.NewCond(&q.mu)
	for range workers {
		q.wg.Go(q.worker)
	}
	return q
}

func (q *queue) schedule(key string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return ErrClosed
	}
	switch {
	case q.running[key]:
		q.again[key] = true
	case !q.pending[key]:
		q.pending[key] = true
		q.order = append(q.order, key)
		q.cond.Signal()
	}
	return nil
}

// next blocks for a runnable key; ok=false once closed and drained.
func (q *queue) next() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for {
		for i, k := range q.order {
			if q.running[k] {
				continue // re-queued while running: wait for that run
			}
			q.order = append(q.order[:i:i], q.order[i+1:]...)
			delete(q.pending, k)
			q.running[k] = true
			return k, true
		}
		if q.closed && len(q.order) == 0 {
			return "", false
		}
		q.cond.Wait()
	}
}

func (q *queue) done(key string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.running, key)
	if q.again[key] {
		delete(q.again, key)
		if !q.pending[key] {
			q.pending[key] = true
			q.order = append(q.order, key)
		}
	}
	q.cond.Broadcast()
}

func (q *queue) worker() {
	for {
		k, ok := q.next()
		if !ok {
			return
		}
		q.run(context.Background(), k)
		q.done(k)
	}
}

// close stops accepting work, lets the workers drain everything queued
// (including re-runs) and waits for them.
func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
	q.wg.Wait()
}
