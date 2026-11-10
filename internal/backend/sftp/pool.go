package sftp

import (
	"context"
	"log/slog"
	"sync"

	"fileload/internal/model"
)

// Pool shares one Client per identical profile. Every user (the browse
// connection, each mount) holds a Lease; the connection closes when the
// last lease is released.
type Pool struct {
	log *slog.Logger

	mu      sync.Mutex
	entries map[model.Server]*poolEntry
}

type poolEntry struct {
	client *Client
	refs   int
}

func NewPool(log *slog.Logger) *Pool {
	return &Pool{log: log, entries: map[model.Server]*poolEntry{}}
}

// Lease is one reference to a pooled client. Release is idempotent.
type Lease struct {
	*Client
	release func()
}

func (l *Lease) Release() { l.release() }

// Acquire returns a lease on a client for p, dialing if necessary. The key
// includes the credentials, so a changed password yields a new connection.
func (p *Pool) Acquire(ctx context.Context, prof model.Server) (*Lease, error) {
	p.mu.Lock()
	if e, ok := p.entries[prof]; ok {
		e.refs++
		p.mu.Unlock()
		return p.lease(prof, e), nil
	}
	p.mu.Unlock()

	// dial without holding the lock: a slow server must not block others
	c, err := Dial(ctx, prof, p.log)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if e, ok := p.entries[prof]; ok { // lost a race: use the winner
		e.refs++
		p.mu.Unlock()
		_ = c.Close()
		return p.lease(prof, e), nil
	}
	e := &poolEntry{client: c, refs: 1}
	p.entries[prof] = e
	p.mu.Unlock()
	return p.lease(prof, e), nil
}

func (p *Pool) lease(prof model.Server, e *poolEntry) *Lease {
	return &Lease{Client: e.client, release: sync.OnceFunc(func() {
		p.mu.Lock()
		e.refs--
		last := e.refs == 0
		if last {
			delete(p.entries, prof)
		}
		p.mu.Unlock()
		if last {
			_ = e.client.Close()
		}
	})}
}

// Close closes every pooled client regardless of outstanding leases.
func (p *Pool) Close() {
	p.mu.Lock()
	entries := p.entries
	p.entries = map[model.Server]*poolEntry{}
	p.mu.Unlock()
	for _, e := range entries {
		_ = e.client.Close()
	}
}
