package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"testing"
	"time"
)

// pinDriver is a database/sql driver whose connections do nothing, so a test can count how many a pool hands out.
type pinDriver struct{}

type pinConn struct{}

func (pinDriver) Open(string) (driver.Conn, error)  { return pinConn{}, nil }
func (pinConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("not used") }
func (pinConn) Close() error                        { return nil }
func (pinConn) Begin() (driver.Tx, error)           { return nil, errors.New("not used") }

func init() { sql.Register("pintest", pinDriver{}) }

func newPinPool(t *testing.T, n int) *sql.DB {
	t.Helper()
	d, err := sql.Open("pintest", "")
	if err != nil {
		t.Fatal(err)
	}
	d.SetMaxOpenConns(n)
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestPinGateSizeAndRefusal(t *testing.T) {
	d := newPinPool(t, 6)
	if got := gateOf(d).cap; got != 6-pinReserve {
		t.Fatalf("gate size %d, want %d", got, 6-pinReserve)
	}
	var refused []string
	SetPinObserver(func(job string) { refused = append(refused, job) })
	t.Cleanup(func() { SetPinObserver(func(string) {}) })
	var dones []func()
	for range 2 { // two jobs of weight 2 fill 4
		_, done, err := Pin(context.Background(), d, "a", 2)
		if err != nil {
			t.Fatal(err)
		}
		dones = append(dones, done)
	}
	if _, _, err := Pin(context.Background(), d, "b", 1); !errors.Is(err, ErrPinGateFull) {
		t.Fatalf("with every slot taken got %v, want ErrPinGateFull", err)
	}
	if len(refused) != 1 || refused[0] != "b" {
		t.Fatalf("observer saw %v, want [b]", refused)
	}
	dones[0]()
	dones[0]() // done twice must not free twice
	if _, _, err := Pin(context.Background(), d, "b", 2); err != nil {
		t.Fatalf("after a release: %v", err)
	}
	if _, _, err := Pin(context.Background(), d, "b", 2); !errors.Is(err, ErrPinGateFull) {
		t.Fatalf("a double done freed a slot twice: %v", err)
	}
}

func TestPinSmallPoolStillAllowsOne(t *testing.T) {
	d := newPinPool(t, 1)
	_, done, err := Pin(context.Background(), d, "a", 2) // weight is clamped to the gate
	if err != nil {
		t.Fatal(err)
	}
	done()
}

func TestPinUnlimitedPoolHasNoGate(t *testing.T) {
	d := newPinPool(t, 0)
	if gateOf(d) != nil {
		t.Fatal("an unlimited pool must not be gated")
	}
}

func TestPinFreesTheSlotWhenTheConnectionFails(t *testing.T) {
	d := newPinPool(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := range 3 {
		if _, _, err := Pin(ctx, d, "a", 1); err == nil || errors.Is(err, ErrPinGateFull) {
			t.Fatalf("try %d: got %v, want a context error that frees its slot", i, err)
		}
	}
}

// The symptom of rivolt#1998: every maintenance and singleton job starts at once on a small pool, each holds its lock connection (and a
// second one for the pool queries a singleton job makes meanwhile), and a booting pod must still get a connection.
func TestBootGetsAConnectionWhileEveryJobIsRunning(t *testing.T) {
	for _, size := range []int{3, 6} {
		t.Run(string(rune('0'+size)), func(t *testing.T) {
			d := newPinPool(t, size)
			hold := make(chan struct{})
			var wg sync.WaitGroup
			started := make(chan struct{}, 20)
			job := func(name string, weight int) {
				defer wg.Done()
				conn, done, err := Pin(context.Background(), d, name, weight)
				started <- struct{}{}
				if err != nil {
					return // skipped, as the real jobs do
				}
				defer done()
				_ = conn
				var work []*sql.Conn
				for range weight - 1 { // the pool query a singleton job makes while it holds its lock
					c, err := d.Conn(context.Background())
					if err != nil {
						t.Error(err)
						return
					}
					work = append(work, c)
				}
				<-hold
				for _, c := range work {
					_ = c.Close()
				}
			}
			jobs := []struct {
				name   string
				weight int
			}{{"retention", 1}, {"thin", 1}, {"archive", 1}, {"drain_leader", 1}, {"packhealth", 2}, {"vehicle_discovery", 2}, {"x", 2}, {"y", 2}}
			for _, j := range jobs {
				wg.Add(1)
				go job(j.name, j.weight)
			}
			for range jobs {
				<-started
			}
			time.Sleep(50 * time.Millisecond) // let the work connections be taken
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			boot, err := d.Conn(ctx) // settings.NewManager's first query
			if err != nil {
				t.Fatalf("boot did not get a connection with every job running: %v", err)
			}
			_ = boot.Close()
			close(hold)
			wg.Wait()
		})
	}
}
