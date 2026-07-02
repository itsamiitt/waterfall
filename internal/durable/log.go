// Package durable provides crash-safe job delivery: a file-backed write-ahead Log, a
// DurableStore that applies the transactional-outbox pattern (job state + publish-intent
// committed atomically), and a Relay that moves published intents onto the in-process
// queue. Redeliveries are safe because the Execution Engine is idempotent (gate G2).
//
// This is the concrete, dependency-free realisation of the hand-rolled-saga + outbox
// fallback path (ADR-0013/0014, docs/10 §4). A production build swaps the Log for a
// Kafka/Redpanda topic behind the same append/replay contract.
package durable

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"sync"
)

// commitKind marks the end of an atomically-appended batch. Records appended before a
// commit marker are only applied at replay once their commit is seen, so a torn tail
// (a crash mid-append) drops a whole partial batch rather than half of it.
const commitKind = "__commit__"

// Record is one durable entry. Seq is assigned by the Log on append (monotonic).
type Record struct {
	Seq  uint64          `json:"seq"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data,omitempty"`
}

// Log is an append-only, fsync'd, framed record log with atomic batches and torn-tail
// recovery. Frame layout: [4-byte payload length][4-byte CRC32(payload)][payload JSON].
type Log struct {
	mu      sync.Mutex
	f       *os.File
	offset  int64  // byte offset of the end of the last COMMITTED batch
	nextSeq uint64 // next sequence number to assign
}

// Open opens (or creates) the log at path, replaying it to recover committed records and
// truncating any torn tail left by a crash. fn is invoked for each committed record in
// order during recovery; pass nil to just recover offsets.
func Open(path string, fn func(Record) error) (*Log, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Log{f: f}
	if err := l.replay(fn); err != nil {
		_ = f.Close()
		return nil, err
	}
	return l, nil
}

// Append writes recs as one atomic batch: all records plus a trailing commit marker are
// serialized and written with a single Sync. On a crash mid-write, replay discards the
// entire uncommitted batch. Seq numbers are assigned in order.
func (l *Log) Append(recs ...Record) error {
	if len(recs) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	buf := make([]byte, 0, 256)
	for i := range recs {
		recs[i].Seq = l.nextSeq
		l.nextSeq++
		frame, err := encodeFrame(recs[i])
		if err != nil {
			return err
		}
		buf = append(buf, frame...)
	}
	commit, err := encodeFrame(Record{Seq: l.nextSeq, Kind: commitKind})
	if err != nil {
		return err
	}
	l.nextSeq++
	buf = append(buf, commit...)

	// Write at the end of the last committed batch (defends against a prior torn tail
	// that replay already truncated).
	if _, err := l.f.WriteAt(buf, l.offset); err != nil {
		return err
	}
	if err := l.f.Sync(); err != nil {
		return err
	}
	l.offset += int64(len(buf))
	return nil
}

// Close syncs and closes the underlying file.
func (l *Log) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return nil
	}
	err := l.f.Close()
	l.f = nil
	return err
}

func (l *Log) replay(fn func(Record) error) error {
	if _, err := l.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(l.f)

	var committedOffset int64 // end of the last good commit
	var pos int64             // bytes consumed so far
	var maxSeq uint64
	var batch []Record

	for {
		rec, n, err := readFrame(r)
		if err != nil {
			// Torn or corrupt frame: stop. Everything after committedOffset is an
			// uncommitted/torn tail and is discarded.
			break
		}
		pos += n
		if rec.Seq >= maxSeq {
			maxSeq = rec.Seq + 1
		}
		if rec.Kind == commitKind {
			// Apply the buffered batch atomically.
			for _, br := range batch {
				if fn != nil {
					if err := fn(br); err != nil {
						return err
					}
				}
			}
			batch = batch[:0]
			committedOffset = pos
			continue
		}
		batch = append(batch, rec)
	}

	l.offset = committedOffset
	l.nextSeq = maxSeq
	// Truncate any torn/uncommitted tail so future appends start clean.
	if err := l.f.Truncate(committedOffset); err != nil {
		return err
	}
	if _, err := l.f.Seek(committedOffset, io.SeekStart); err != nil {
		return err
	}
	return nil
}

func encodeFrame(rec Record) ([]byte, error) {
	payload, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	if len(payload) > int(^uint32(0)) {
		return nil, errors.New("durable: record too large")
	}
	frame := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(frame[0:4], uint32(len(payload)))
	binary.BigEndian.PutUint32(frame[4:8], crc32.ChecksumIEEE(payload))
	copy(frame[8:], payload)
	return frame, nil
}

// readFrame reads one frame, returning the record and the number of bytes consumed. A
// short read or CRC mismatch returns an error (treated by replay as the torn tail).
func readFrame(r *bufio.Reader) (Record, int64, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Record{}, 0, err
	}
	n := binary.BigEndian.Uint32(hdr[0:4])
	want := binary.BigEndian.Uint32(hdr[4:8])
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Record{}, 0, err
	}
	if crc32.ChecksumIEEE(payload) != want {
		return Record{}, 0, errors.New("durable: crc mismatch")
	}
	var rec Record
	if err := json.Unmarshal(payload, &rec); err != nil {
		return Record{}, 0, err
	}
	return rec, int64(8 + n), nil
}
