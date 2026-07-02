package durable

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func raw(s string) json.RawMessage { return json.RawMessage(`"` + s + `"`) }

func collect(dst *[]string) func(Record) error {
	return func(r Record) error {
		*dst = append(*dst, r.Kind+":"+string(r.Data))
		return nil
	}
}

func TestLog_AppendReplayRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	l, err := Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Record{Kind: "a", Data: raw("1")}, Record{Kind: "b", Data: raw("2")}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(Record{Kind: "c", Data: raw("3")}); err != nil {
		t.Fatal(err)
	}
	l.Close()

	var got []string
	l2, err := Open(path, collect(&got))
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	want := []string{`a:"1"`, `b:"2"`, `c:"3"`}
	if len(got) != len(want) {
		t.Fatalf("replay got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("replay[%d]=%s want %s", i, got[i], want[i])
		}
	}
}

// TestLog_TornTailRecovery proves a crash that leaves a partially-written frame is
// recovered: the torn tail is dropped and the log remains appendable.
func TestLog_TornTailRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	l, _ := Open(path, nil)
	_ = l.Append(Record{Kind: "a", Data: raw("1")})
	_ = l.Append(Record{Kind: "b", Data: raw("2")})
	l.Close()

	// Simulate a torn write: a frame header claiming 50 payload bytes but only 3 present.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.Write([]byte{0, 0, 0, 50, 9, 9, 9, 9, 1, 2, 3})
	f.Close()

	var got []string
	l2, err := Open(path, collect(&got))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("torn tail not dropped: got %v", got)
	}
	// The log must still be appendable after recovery, and replay clean afterwards.
	if err := l2.Append(Record{Kind: "c", Data: raw("3")}); err != nil {
		t.Fatal(err)
	}
	l2.Close()

	var got2 []string
	l3, _ := Open(path, collect(&got2))
	defer l3.Close()
	if len(got2) != 3 || got2[2] != `c:"3"` {
		t.Fatalf("append after recovery failed: %v", got2)
	}
}

// TestLog_UncommittedBatchDropped proves atomic batches: records written without their
// trailing commit marker (a crash mid-batch) are not applied.
func TestLog_UncommittedBatchDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	l, _ := Open(path, nil)
	_ = l.Append(Record{Kind: "a", Data: raw("1")}) // committed batch
	l.Close()

	// Append a valid frame for "b" but NO commit marker (crash before commit).
	frame, err := encodeFrame(Record{Seq: 99, Kind: "b", Data: raw("2")})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.Write(frame)
	f.Close()

	var got []string
	l2, _ := Open(path, collect(&got))
	defer l2.Close()
	if len(got) != 1 || got[0] != `a:"1"` {
		t.Fatalf("uncommitted batch must be dropped, got %v", got)
	}
}
