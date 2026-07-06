//go:build integration

package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/enrichment/waterfall/internal/pg"
)

// TestConn_ListenNotify proves the P7 LISTEN/NOTIFY extension (ADR-0019 timebox): the async
// 'A' NotificationResponse path on a dedicated conn — both the idle WaitNotification wait and
// the buffered-during-query path — plus ctx cancellation and channel-name validation.
func TestConn_ListenNotify(t *testing.T) {
	listener, err := pg.Connect(dsn(t))
	if err != nil {
		t.Fatalf("connect listener: %v", err)
	}
	defer listener.Close()
	sender, err := pg.Connect(dsn(t))
	if err != nil {
		t.Fatalf("connect sender: %v", err)
	}
	defer sender.Close()

	if err := listener.Listen("dash_config"); err != nil {
		t.Fatalf("listen: %v", err)
	}
	if err := listener.Listen("bad name; drop table x"); err == nil {
		t.Fatal("Listen accepted an invalid channel name")
	}

	// (1) Idle wait: notification arrives while blocked in WaitNotification.
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = sender.Exec("select pg_notify('dash_config', 'ping-1')")
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	n, err := listener.WaitNotification(ctx)
	cancel()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if n.Channel != "dash_config" || n.Payload != "ping-1" {
		t.Fatalf("notification = %+v", n)
	}

	// (2) Buffered path: a notification consumed while reading an ordinary query reply is
	// retained and drained by the next WaitNotification without touching the socket.
	if err := sender.Exec("select pg_notify('dash_config', 'ping-2')"); err != nil {
		t.Fatalf("notify: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // let the async message reach the listener's socket
	if _, err := listener.Query("select 1"); err != nil {
		t.Fatalf("interleaved query: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	n, err = listener.WaitNotification(ctx)
	cancel()
	if err != nil {
		t.Fatalf("wait buffered: %v", err)
	}
	if n.Payload != "ping-2" {
		t.Fatalf("buffered payload = %q, want ping-2", n.Payload)
	}

	// (3) The connection still runs statements normally after deadline-polled waits.
	res, err := listener.QueryParams("select $1::int + 1", 41)
	if err != nil || len(res.Rows) != 1 || *res.Rows[0][0] != "42" {
		t.Fatalf("post-wait query = %+v, %v", res, err)
	}

	// (4) ctx cancellation surfaces promptly with no notification pending.
	ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
	start := time.Now()
	_, err = listener.WaitNotification(ctx)
	cancel()
	if err == nil {
		t.Fatal("expected ctx cancellation error")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("cancellation took %v, want prompt", time.Since(start))
	}
}
