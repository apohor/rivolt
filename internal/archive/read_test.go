package archive

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
)

// A read cut short by the network looks like a truncated object; the reader
// reads it again before calling it corrupt (DA on #618). Only an object that
// fails twice is reported.
func TestEachRowRetriesBeforeCallingCorrupt(t *testing.T) {
	uid, vid := uuid.New(), uuid.New()
	month := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	key := bytes.Repeat([]byte{9}, KeySize)
	obj, _ := object(t, key, ObjectAAD(uid, vid, MonthString(month)), csvRows(50000))
	if len(obj) < 3*ChunkSize {
		t.Fatalf("object is %d bytes; the test needs several chunks", len(obj))
	}
	cut := obj[:headerSize+2*sealedChunk+100]
	for _, tc := range []struct {
		name    string
		reads   [][]byte // what each GET returns, in turn
		corrupt int      // OnCorrupt calls expected
		wantErr error
	}{
		{"whole", [][]byte{obj}, 0, nil},
		{"cut once, whole on retry", [][]byte{cut, obj}, 0, ErrInterrupted},
		{"cut twice", [][]byte{cut, cut}, 1, ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, calls := 0, 0
			r := &Reader{env: "test"}
			r.key = func(context.Context, uuid.UUID) ([]byte, error) { return key, nil }
			r.get = func(context.Context, string) (io.ReadCloser, error) {
				b := tc.reads[n]
				if n < len(tc.reads)-1 {
					n++
				}
				return io.NopCloser(bytes.NewReader(b)), nil
			}
			r.OnCorrupt = func(uuid.UUID, uuid.UUID, time.Time, error) { calls++ }
			rows := 0
			err := r.EachRow(context.Background(), uid, vid, month, func(Row) error { rows++; return nil })
			if tc.wantErr == nil && err != nil || tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("err %v, want %v", err, tc.wantErr)
			}
			if calls != tc.corrupt {
				t.Errorf("OnCorrupt called %d times, want %d", calls, tc.corrupt)
			}
			if tc.wantErr == nil && rows != 50000 {
				t.Errorf("%d rows, want 50000", rows)
			}
		})
	}
}
