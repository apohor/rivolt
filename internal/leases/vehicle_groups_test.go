package leases

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

type noDBConnector struct{}

func (noDBConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("no database in this test")
}
func (noDBConnector) Driver() driver.Driver { return nil }

// realFenceStore runs the Coordinator's real lead pass against the leaderStore fake for the database calls, but answers Fence and
// Deadline from a real Store, fed only by what the coordinator hands it. LeadGroups does what the real one does to the Store's state:
// the group becomes held and its renew is stamped.
type realFenceStore struct {
	*leaderStore
	real *Store
	log  []string
}

func (r *realFenceStore) LeadGroups(ctx context.Context) (GroupLead, error) {
	res, err := r.leaderStore.LeadGroups(ctx)
	if err != nil {
		return res, err
	}
	r.real.groupMu.Lock()
	r.real.groups = map[string]int64{}
	for g, e := range res.Held {
		r.real.groups[g] = e
	}
	r.real.groupOKAt = time.Now()
	r.real.groupMu.Unlock()
	return res, nil
}
func (r *realFenceStore) SetVehicleGroups(vg map[string]string) {
	r.log = append(r.log, "setVehicleGroups")
	r.real.SetVehicleGroups(vg)
}

func newRealFenceStore(t *testing.T, vg map[string]string, held map[string]int64) *realFenceStore {
	t.Helper()
	db := sql.OpenDB(noDBConnector{})
	t.Cleanup(func() { _ = db.Close() })
	real, err := NewStore(db, "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	real.SetGroupLead(true)
	return &realFenceStore{leaderStore: &leaderStore{fakeStore: newFakeStore(), vg: vg, res: GroupLead{Held: held}}, real: real}
}

// qa's preview finding: the pod that held every group refused all of its own redis and samples writes (epoch_last_held 0), because
// nothing in production ever gave the Store its vehicle -> group map, so every vehicle read Lost. The unit tests had passed because they
// set the map by hand. This one runs the real pass, hands the Store nothing, and asks the Store.
func TestLeadPassGivesTheStoreItsVehicleMapSoTheOwnerIsHeld(t *testing.T) {
	// A live vehicle and a demo (source-fed) one, in two groups this pod holds, under the ids the monitors use.
	vg := map[string]string{"01-live-0001": "g1", "demo-vehicle-1": "g2"}
	rs := newRealFenceStore(t, vg, map[string]int64{"g1": 4, "g2": 9})
	var acquired []string
	c := newCoordinator(rs, func(context.Context) ([]string, error) { return []string{"01-live-0001", "demo-vehicle-1"}, nil },
		func(v string) {
			// The first write after an acquire must already be Held: by the time the vehicle's monitor is told, the map is installed.
			if _, st := rs.real.Fence(v); st != fence.Held {
				t.Errorf("%s is %v when its subscription starts, want Held", v, st)
			}
			acquired = append(acquired, v)
		}, func(string) {}, nil, time.Hour)
	c.jitter = time.Second
	c.reconcile(context.Background())

	if len(acquired) != 2 {
		t.Fatalf("acquired %v", acquired)
	}
	for v, wantEpoch := range map[string]int64{"01-live-0001": 4, "demo-vehicle-1": 9} {
		tok, st := rs.real.Fence(v)
		if st != fence.Held || tok.Epoch != wantEpoch || tok.Pod != "pod-a" {
			t.Errorf("Fence(%s) = %v %+v, want Held at epoch %d", v, st, tok, wantEpoch)
		}
		at, ok := rs.real.Deadline(v)
		if !ok || !at.After(time.Now()) {
			t.Errorf("Deadline(%s) = %v %v, want a deadline in the future", v, at, ok)
		}
	}
	if _, st := rs.real.Fence("unknown-vehicle"); st != fence.Lost {
		t.Errorf("a vehicle in no group = %v, want Lost", st)
	}

	// A vehicle added between passes is Held at its first write after the pass that acquires it.
	rs.leaderStore.vg["01-live-0002"] = "g1"
	c2vehicles := []string{"01-live-0001", "demo-vehicle-1", "01-live-0002"}
	c.vehicles = func(context.Context) ([]string, error) { return c2vehicles, nil }
	c.onAcquire = func(v string) {
		if v == "01-live-0002" {
			if _, st := rs.real.Fence(v); st != fence.Held {
				t.Errorf("the added vehicle is %v at its first write, want Held", st)
			}
		}
	}
	if _, st := rs.real.Fence("01-live-0002"); st != fence.Lost {
		t.Fatalf("before the next pass the added vehicle is %v (sanity: not yet in the map)", st)
	}
	c.reconcile(context.Background())
	if _, st := rs.real.Fence("01-live-0002"); st != fence.Held {
		t.Errorf("after the next pass the added vehicle is %v, want Held", st)
	}

	// A pass that cannot read the map keeps the last one (the group must still be held and renewed to count).
	rs.leaderStore.lmu.Lock()
	rs.leaderStore.vgErr = errors.New("db blip")
	rs.leaderStore.lmu.Unlock()
	c.reconcile(context.Background())
	if _, st := rs.real.Fence("01-live-0001"); st != fence.Held {
		t.Errorf("after a failed map read: %v, want Held (last map kept)", st)
	}
}

// With lead mode off the Store answers Off before it looks anything up, so prod (flag off) is unchanged whatever the map holds.
func TestFenceIsOffWithLeadModeOffWhateverTheMapHolds(t *testing.T) {
	db := sql.OpenDB(noDBConnector{})
	defer db.Close()
	s, err := NewStore(db, "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, st := s.Fence("v"); st != fence.Off {
		t.Errorf("empty: %v, want Off", st)
	}
	s.SetVehicleGroups(map[string]string{"v": "g1"})
	if _, st := s.Fence("v"); st != fence.Off {
		t.Errorf("with a map: %v, want Off", st)
	}
	if _, ok := s.Deadline("v"); ok {
		t.Error("a deadline with lead mode off")
	}
}

func TestSetVehicleGroupsCopiesItsArgument(t *testing.T) {
	db := sql.OpenDB(noDBConnector{})
	defer db.Close()
	s, _ := NewStore(db, "pod-a")
	s.SetGroupLead(true)
	m := map[string]string{"v": "g1"}
	s.SetVehicleGroups(m)
	m["v"] = "g2"
	delete(m, "v")
	s.groupMu.Lock()
	got := s.vehicleGroup["v"]
	s.groupMu.Unlock()
	if got != "g1" {
		t.Errorf("the store saw the caller's later edits: %q", got)
	}
}

// The store interface the lead pass needs includes SetVehicleGroups: a store without it is not a lead-mode store at all (it does not
// compile as one), instead of the pass skipping the call and every vehicle reading Lost. And the pass hands the map over, to a fake
// that only records it.
func TestLeadPassHandsTheMapToTheStoreInterface(t *testing.T) {
	ls := &leaderStore{fakeStore: newFakeStore(), vg: map[string]string{"v1": "g1"}, res: GroupLead{Held: map[string]int64{"g1": 1}}}
	var gl groupLeader = ls // fails to compile if SetVehicleGroups leaves the interface
	_ = gl
	c := newLeadCoord(ls, []string{"v1"})
	c.reconcile(context.Background())
	ls.lmu.Lock()
	got := ls.setVG
	ls.lmu.Unlock()
	if got["v1"] != "g1" {
		t.Fatalf("the pass handed %v to the store, want v1 -> g1", got)
	}
}
