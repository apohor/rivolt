package leases

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

type countingConnector struct{ n atomic.Int64 }

func (c *countingConnector) Connect(context.Context) (driver.Conn, error) {
	c.n.Add(1)
	return nil, errors.New("no database in this test")
}
func (c *countingConnector) Driver() driver.Driver { return nil }

// Fence is asked once per frame: it must answer from memory, with no database round trip.
func TestFenceNeverTouchesTheDatabase(t *testing.T) {
	cc := &countingConnector{}
	db := sql.OpenDB(cc)
	defer db.Close()
	s, err := NewStore(db, "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	s.SetGroupLead(true)
	s.SetVehicleGroups(map[string]string{"v1": "g1", "v2": "g2"})
	s.groupMu.Lock()
	s.groups = map[string]int64{"g1": 7}
	s.groupOKAt = time.Now()
	s.stolenAt["g1"] = time.Now()
	s.groupMu.Unlock()

	if _, st := s.Fence("v1"); st != fence.Warming {
		t.Errorf("v1 = %v, want Warming", st)
	}
	if _, st := s.Fence("v2"); st != fence.Lost {
		t.Errorf("v2 (group not held) = %v, want Lost", st)
	}
	if _, st := s.Fence("nope"); st != fence.Lost {
		t.Errorf("unknown vehicle = %v, want Lost", st)
	}
	s.groupMu.Lock()
	delete(s.stolenAt, "g1")
	s.groupMu.Unlock()
	if tok, st := s.Fence("v1"); st != fence.Held || tok.Epoch != 7 || tok.Pod != "pod-a" || tok.Group != "g1" {
		t.Errorf("v1 = %v %+v, want Held with the epoch", st, tok)
	}
	s.groupMu.Lock()
	s.groupOKAt = time.Now().Add(-100 * time.Second) // renew sent longer ago than the local deadline (80 s of a 2 min lease, 20 s of 30 s)
	s.groupMu.Unlock()
	if _, st := s.Fence("v1"); st != fence.Lost {
		t.Errorf("past the local deadline = %v, want Lost", st)
	}
	for i := 0; i < 1000; i++ {
		s.Fence("v1")
	}
	if n := cc.n.Load(); n != 0 {
		t.Fatalf("Fence opened %d database connections, want 0", n)
	}
}

func TestStoreDeadline(t *testing.T) {
	db := sql.OpenDB(&countingConnector{})
	defer db.Close()
	s, err := NewStore(db, "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Deadline("v1"); ok {
		t.Fatal("lead mode off must have no deadline")
	}
	s.SetGroupLead(true)
	s.SetVehicleGroups(map[string]string{"v1": "g1", "v2": "g2"})
	sent := time.Now().Add(-5 * time.Second)
	s.groupMu.Lock()
	s.groups = map[string]int64{"g1": 7}
	s.groupOKAt = sent
	s.groupMu.Unlock()
	d, ok := s.Deadline("v1")
	if !ok || !d.Equal(sent.Add(s.localDeadline())) {
		t.Errorf("held: %v %v, want the renew's send time + the local deadline", d, ok)
	}
	if d, ok := s.Deadline("v2"); !ok || time.Now().Before(d) {
		t.Errorf("a group not held must have a deadline in the past, got %v %v", d, ok)
	}
}
