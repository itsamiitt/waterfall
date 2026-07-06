package pg

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

// LISTEN/NOTIFY support (ADR-0019 timeboxed extension, doc 12 §P7): the async 'A'
// NotificationResponse message is handled in the reply loops (conn.go) so notifications
// arriving mid-statement are buffered, and WaitNotification blocks for the next one on an
// otherwise-idle connection. Use a DEDICATED non-pooled Conn for listening — a Conn is not
// safe for concurrent use, and a pooled connection's notifications would be observed by
// whichever caller holds it.

// ErrNotListening is returned by WaitNotification on a context cancellation.
var ErrNotListening = errors.New("pg: wait notification cancelled")

// bufferNotification parses and stores an 'A' NotificationResponse body:
// int32 pid, cstring channel, cstring payload.
func (c *Conn) bufferNotification(body []byte) {
	if len(body) < 6 {
		return
	}
	pid := binary.BigEndian.Uint32(body[:4])
	rest := body[4:]
	channel, rest := readCString(rest)
	payload, _ := readCString(rest)
	c.notifications = append(c.notifications, Notification{PID: pid, Channel: channel, Payload: payload})
}

func readCString(b []byte) (string, []byte) {
	for i := 0; i < len(b); i++ {
		if b[i] == 0 {
			return string(b[:i]), b[i+1:]
		}
	}
	return string(b), nil
}

// Listen subscribes this connection to a NOTIFY channel. The channel name must be a plain
// identifier (letters, digits, underscore) — it is quoted, never interpolated as SQL.
func (c *Conn) Listen(channel string) error {
	if !validChannelName(channel) {
		return fmt.Errorf("pg: invalid LISTEN channel name %q", channel)
	}
	return c.Exec(`listen "` + channel + `"`)
}

// Unlisten unsubscribes the connection from a channel.
func (c *Conn) Unlisten(channel string) error {
	if !validChannelName(channel) {
		return fmt.Errorf("pg: invalid channel name %q", channel)
	}
	return c.Exec(`unlisten "` + channel + `"`)
}

func validChannelName(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, ch := range s {
		switch {
		case ch >= 'a' && ch <= 'z', ch == '_':
		case ch >= '0' && ch <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// WaitNotification blocks until a notification arrives on this connection (or ctx is done).
// Notifications buffered during earlier statement replies are drained first. It polls the
// socket with short read deadlines so ctx cancellation is honored within ~250ms; a read
// deadline never corrupts the protocol because bufio.Peek retains partially-buffered bytes.
func (c *Conn) WaitNotification(ctx context.Context) (Notification, error) {
	for {
		if len(c.notifications) > 0 {
			n := c.notifications[0]
			c.notifications = c.notifications[1:]
			return n, nil
		}
		if err := ctx.Err(); err != nil {
			return Notification{}, fmt.Errorf("%w: %w", ErrNotListening, err)
		}
		_ = c.c.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		if _, err := c.r.Peek(5); err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			_ = c.c.SetReadDeadline(time.Time{})
			return Notification{}, err
		}
		// A full message header is buffered; read the message with a generous bound.
		_ = c.c.SetReadDeadline(time.Now().Add(5 * time.Second))
		typ, body, err := c.readMsg()
		_ = c.c.SetReadDeadline(time.Time{})
		if err != nil {
			return Notification{}, err
		}
		switch typ {
		case 'A':
			c.bufferNotification(body)
		case 'E':
			return Notification{}, parseError(body)
		default:
			// ParameterStatus / Notice / anything else on an idle conn: ignore.
		}
	}
}
