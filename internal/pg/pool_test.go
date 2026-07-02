package pg

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestPool_BoundsAndReuse(t *testing.T) {
	var opens int32
	dial := func() (*Conn, error) {
		atomic.AddInt32(&opens, 1)
		return &Conn{}, nil // fake conn; Close is nil-safe
	}
	p := NewPoolWithDialer(dial, 2)
	ctx := context.Background()

	c1, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&opens); got != 2 {
		t.Fatalf("want 2 opens, got %d", got)
	}

	// Cap reached, none idle: Get must not open a 3rd; a cancelled ctx returns promptly.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := p.Get(cctx); err == nil {
		t.Fatal("Get should block (then ctx-cancel) when the pool is exhausted")
	}
	if got := atomic.LoadInt32(&opens); got != 2 {
		t.Fatalf("exhausted pool must not open beyond cap, opens=%d", got)
	}

	// Returning a connection lets the next Get reuse it (no new dial).
	p.Put(c1, false)
	c3, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&opens); got != 2 {
		t.Fatalf("reuse must not dial, opens=%d", got)
	}

	// A broken connection is closed and its token returned (a new Get may dial again).
	p.Put(c2, true)
	c4, err := p.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&opens); got != 3 {
		t.Fatalf("after a broken conn, a fresh Get should dial, opens=%d", got)
	}

	p.Put(c3, false)
	p.Put(c4, false)
	p.Close()
}
