package pg

import (
	"context"
	"sync"
)

// Pool is a small bounded connection pool. Each pgstore operation checks out a connection,
// runs one transaction (which binds the tenant GUC), then returns it — so a pooled
// connection is never shared across tenants mid-transaction.
//
// A token bounds the number of OPEN connections to max; a connection kept in `free` retains
// its token (stays open for reuse), and closing a connection returns its token.
type Pool struct {
	dial   func() (*Conn, error)
	free   chan *Conn
	tokens chan struct{}

	mu     sync.Mutex
	closed bool
}

// NewPool builds a pool that dials cfg, capped at max open connections.
func NewPool(cfg Config, max int) *Pool {
	return NewPoolWithDialer(func() (*Conn, error) { return Connect(cfg) }, max)
}

// NewPoolWithDialer builds a pool with a custom dialer (used in tests).
func NewPoolWithDialer(dial func() (*Conn, error), max int) *Pool {
	if max < 1 {
		max = 1
	}
	t := make(chan struct{}, max)
	for i := 0; i < max; i++ {
		t <- struct{}{}
	}
	return &Pool{dial: dial, free: make(chan *Conn, max), tokens: t}
}

// Get returns a connection, reusing an idle one or opening a new one up to the cap. It
// blocks (honoring ctx) when the cap is reached and none is idle.
func (p *Pool) Get(ctx context.Context) (*Conn, error) {
	// fast path: an idle connection is ready
	select {
	case c := <-p.free:
		return c, nil
	default:
	}
	select {
	case c := <-p.free:
		return c, nil
	case <-p.tokens: // permission to open a new connection
		c, err := p.dial()
		if err != nil {
			p.returnToken()
			return nil, err
		}
		return c, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Put returns a connection to the pool, or closes it (freeing its token) if it is broken or
// the idle buffer is full.
func (p *Pool) Put(c *Conn, broken bool) {
	if c == nil {
		return
	}
	if broken {
		c.Close()
		p.returnToken()
		return
	}
	select {
	case p.free <- c:
	default:
		c.Close()
		p.returnToken()
	}
}

func (p *Pool) returnToken() {
	select {
	case p.tokens <- struct{}{}:
	default:
	}
}

// Close closes all idle connections. Connections currently checked out are closed on Put.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()
	for {
		select {
		case c := <-p.free:
			c.Close()
		default:
			return
		}
	}
}
