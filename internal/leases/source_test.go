package leases

import (
	"context"
	"errors"
	"sort"
	"testing"
)

func sortedSrc(t *testing.T, src func(context.Context) ([]string, error)) []string {
	t.Helper()
	out, err := src(context.Background())
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	sort.Strings(out)
	return out
}

// TestVehicleSourceMonitorOnly is the regression test for the
// v0.15.0/v0.16.0 bug: subscriptions never started because the
// composer didn't include the in-memory monitor list as a source.
// At single-pod boot, `vehicles` is empty (it's populated lazily
// by the recorder); without the monitor source, the coordinator's
// vehicle set is empty and onAcquire never fires.
func TestVehicleSourceMonitorOnly(t *testing.T) {
	t.Parallel()
	src := NewVehicleSource(
		func() []string { return []string{"v1", "v2"} },
		nil, // no DB sources — simulates fresh boot, empty `vehicles`
	)
	if got, want := sortedSrc(t, src), []string{"v1", "v2"}; !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestVehicleSourceUnionsAndDedupes(t *testing.T) {
	t.Parallel()
	src := NewVehicleSource(
		func() []string { return []string{"v1", "v2"} },
		nil,
		func(context.Context) ([]string, error) { return []string{"v2", "v3"}, nil }, // overlap with monitor
		func(context.Context) ([]string, error) { return []string{"v3", "v4"}, nil }, // overlap with prev source
	)
	if got, want := sortedSrc(t, src), []string{"v1", "v2", "v3", "v4"}; !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestVehicleSourceDBErrorFallsThrough(t *testing.T) {
	t.Parallel()
	// One DB source errors — monitor + healthy source still
	// contribute, so a Postgres blip can't black-hole the
	// reconcile.
	src := NewVehicleSource(
		func() []string { return []string{"v1"} },
		nil,
		func(context.Context) ([]string, error) { return nil, errors.New("connection refused") },
		func(context.Context) ([]string, error) { return []string{"v2"}, nil },
	)
	if got, want := sortedSrc(t, src), []string{"v1", "v2"}; !equalStrings(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestVehicleSourceDropsEmptyAndAllErrors(t *testing.T) {
	t.Parallel()
	// Empty strings get filtered (ON CONFLICT or NULL'd rows). All
	// sources erroring still returns a non-nil empty slice rather
	// than nil-panicking the caller.
	src := NewVehicleSource(
		func() []string { return []string{"", "v1", ""} },
		nil,
		func(context.Context) ([]string, error) { return nil, errors.New("x") },
	)
	got, err := src(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(got) != 1 || got[0] != "v1" {
		t.Fatalf("got %v, want [v1]", got)
	}
}

func TestVehicleSourceNoSourcesReturnsEmpty(t *testing.T) {
	t.Parallel()
	src := NewVehicleSource(nil, nil)
	out, err := src(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("got %v, want empty", out)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
