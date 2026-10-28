package settings

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// sharedKV is the database several replicas share: a map with a version.
type sharedKV struct {
	mu   sync.Mutex
	m    map[string]string
	ver  int
	fail bool
}

func newSharedKV() *sharedKV { return &sharedKV{m: map[string]string{}} }

func (k *sharedKV) GetAll(context.Context) (map[string]string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string]string, len(k.m))
	for a, b := range k.m {
		out[a] = b
	}
	return out, nil
}
func (k *sharedKV) Set(_ context.Context, key, v string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = v
	k.ver++
	return nil
}
func (k *sharedKV) Delete(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	k.ver++
	return nil
}
func (k *sharedKV) Version(context.Context) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail {
		return "", fmt.Errorf("db down")
	}
	return fmt.Sprint(k.ver), nil
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", what)
}

// Three replicas: the operator turns recap weather off on A and B and C stop sending
// drive coordinates to Open-Meteo within the poll interval, instead of at their next restart.
func TestRecapWeatherOffOnOneReplicaReachesTheOthers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kv := newSharedKV()
	kv.m[keyRecapWeatherEnabled] = "true"
	var pods [3]*Manager
	for i := range pods {
		m, err := NewManager(ctx, kv, AIConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if !m.RecapWeatherEnabled() {
			t.Fatalf("replica %d should start with weather on", i)
		}
		pods[i] = m
		go m.Sync(ctx, 10*time.Millisecond)
	}
	off := false
	if _, err := pods[0].UpdateRecap(ctx, RecapUpdate{WeatherEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 3; i++ {
		i := i
		waitFor(t, fmt.Sprintf("replica %d still has recap weather on", i), func() bool { return !pods[i].RecapWeatherEnabled() })
	}
	// And back on, from another replica.
	on := true
	if _, err := pods[2].UpdateRecap(ctx, RecapUpdate{WeatherEnabled: &on}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		i := i
		waitFor(t, fmt.Sprintf("replica %d did not see weather turned back on", i), func() bool { return pods[i].RecapWeatherEnabled() })
	}
}

// A database that cannot be read changes nothing, and the next successful read catches up.
func TestSettingsSyncKeepsTheCopyWhenTheVersionCannotBeRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kv := newSharedKV()
	kv.m[keyRecapWeatherEnabled] = "true"
	m, err := NewManager(ctx, kv, AIConfig{})
	if err != nil {
		t.Fatal(err)
	}
	go m.Sync(ctx, 10*time.Millisecond)
	kv.mu.Lock()
	kv.fail = true
	kv.mu.Unlock()
	_ = kv.Set(ctx, keyRecapWeatherEnabled, "false")
	time.Sleep(60 * time.Millisecond)
	if !m.RecapWeatherEnabled() {
		t.Fatal("the copy changed although the version could not be read")
	}
	kv.mu.Lock()
	kv.fail = false
	kv.mu.Unlock()
	waitFor(t, "catch up after the database came back", func() bool { return !m.RecapWeatherEnabled() })
}

// Without the poll the others never learn: this is why Sync exists.
func TestWithoutSyncAReplicaKeepsTheOldValue(t *testing.T) {
	ctx := context.Background()
	kv := newSharedKV()
	kv.m[keyRecapWeatherEnabled] = "true"
	a, _ := NewManager(ctx, kv, AIConfig{})
	b, _ := NewManager(ctx, kv, AIConfig{})
	off := false
	if _, err := a.UpdateRecap(ctx, RecapUpdate{WeatherEnabled: &off}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if !b.RecapWeatherEnabled() {
		t.Fatal("the premise changed: a replica learned of the change without Sync")
	}
	if err := b.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if b.RecapWeatherEnabled() {
		t.Fatal("Reload did not pick the change up")
	}
}
