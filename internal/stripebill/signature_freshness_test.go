package stripebill

import (
	"errors"
	"math"
	"testing"
	"time"
)

// A timestamp far enough in the future that its distance from now overflows a Duration must be refused as stale, not slip through the
// freshness window. (Only the holder of the secret can sign one, so this is no forgery; it is the freshness check being exact.)
func TestSignatureTimestampsOutsideADurationAreStale(t *testing.T) {
	body := []byte(`{"id":"evt_far","type":"ping"}`)
	for _, ts := range []int64{math.MaxInt64, math.MaxInt64 / 2, 1_000_000_000_000, 100_000_000_000, -1_000_000_000_000, math.MinInt64 / 2, math.MinInt64} {
		hdr := Sign(body, testSecret, time.Unix(ts, 0))
		if err := VerifySignature(body, hdr, testSecret, t0, SignatureTolerance); !errors.Is(err, ErrStaleTimestamp) {
			t.Errorf("timestamp %d: %v, want ErrStaleTimestamp", ts, err)
		}
	}
	// And the edge of the window itself: exactly at the tolerance is accepted, one second past it is not.
	for _, c := range []struct {
		off  time.Duration
		want error
	}{{SignatureTolerance, nil}, {-SignatureTolerance, nil}, {SignatureTolerance + time.Second, ErrStaleTimestamp}, {-SignatureTolerance - time.Second, ErrStaleTimestamp}} {
		hdr := Sign(body, testSecret, t0.Add(c.off))
		if err := VerifySignature(body, hdr, testSecret, t0, SignatureTolerance); !errors.Is(err, c.want) && !(c.want == nil && err == nil) {
			t.Errorf("offset %v: %v, want %v", c.off, err, c.want)
		}
	}
}

