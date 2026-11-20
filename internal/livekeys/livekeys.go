// Package livekeys serves the design-key list (features.design_preview) from a file the process re-reads while it runs, so turning a key on
// or off needs no pod restart. In the cluster the file is a ConfigMap mounted as a directory (deploy/helm/rivolt, liveKeys.designPreview),
// changed only by a reviewed commit that Argo CD syncs; kubelet refreshes the mount and the poll here picks the change up.
//
// File format: one line, design_preview=a,b,c. An empty value is valid and means every key off. A zero-byte file, a file without the line, or a
// key outside [a-z0-9_] is rejected as a whole and the last good list stays; a pod never falls back to "all off" because of a bad file.
package livekeys

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PollInterval is how often the file is re-read. The path is read afresh each time, so kubelet's ..data symlink swap is followed.
const PollInterval = 10 * time.Second

const fileLine = "design_preview"

// Sources reported on rivolt_live_keys_info.
const (
	SourceFile = "file" // the list came from the mounted file
	SourceEnv  = "env"  // booted without a good file; the list is RIVOLT_DESIGN_PREVIEW (the migration fallback), until the file is good
	SourceNone = "none" // booted without a good file and without env: empty list
)

// SplitList splits a comma-separated key list: spaces are trimmed, empty entries and repeats dropped, order kept. Empty input gives an empty (non-nil) list.
func SplitList(s string) []string {
	keys := []string{}
	seen := map[string]bool{}
	for _, k := range strings.Split(s, ",") {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	return keys
}

// ValidKeys reports the first key outside [a-z0-9_] (or empty).
func ValidKeys(keys []string) error {
	for _, k := range keys {
		if k == "" {
			return errors.New("empty key")
		}
		for _, c := range k {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
				return fmt.Errorf("key %q: only a-z, 0-9 and _ are allowed", k)
			}
		}
	}
	return nil
}

// ParseFile reads the file body. See the package comment for what is valid.
func ParseFile(b []byte) ([]string, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, errors.New("file is empty")
	}
	var val string
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != fileLine {
			return nil, fmt.Errorf("unexpected line %q", line)
		}
		if found {
			return nil, errors.New("design_preview given twice")
		}
		found, val = true, v
	}
	if !found {
		return nil, errors.New("no design_preview line")
	}
	keys := SplitList(val)
	if err := ValidKeys(keys); err != nil {
		return nil, err
	}
	return keys, nil
}

type snapshot struct {
	keys   []string
	sha    string
	source string
}

// Store holds the current list. Keys is safe for any number of concurrent readers.
type Store struct {
	path string
	log  *slog.Logger
	cur  atomic.Pointer[snapshot]

	mu sync.Mutex // serialises Reload

	info   *prometheus.GaugeVec
	errs   prometheus.Counter
	reload prometheus.Gauge
}

// New builds the store and loads once, before the caller serves anything. path may be empty (no file configured): the list is then env, source
// "env" or "none". env is the raw RIVOLT_DESIGN_PREVIEW value; a bad env list is an error the caller treats as fatal, so it never drops silently.
func New(path, env string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	envKeys := SplitList(env)
	if err := ValidKeys(envKeys); err != nil {
		return nil, fmt.Errorf("RIVOLT_DESIGN_PREVIEW: %w", err)
	}
	s := &Store{path: path, log: log,
		info:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "rivolt_live_keys_info", Help: "1 for the design-key list this pod serves; sha is the list's hash, source is file, env or none."}, []string{"sha", "source"}),
		errs:   prometheus.NewCounter(prometheus.CounterOpts{Name: "rivolt_live_keys_errors_total", Help: "Reads of the live-keys file that were rejected (missing, empty or malformed); the last good list stayed."}),
		reload: prometheus.NewGauge(prometheus.GaugeOpts{Name: "rivolt_live_keys_last_reload_timestamp_seconds", Help: "Unix time the served list last changed."}),
	}
	src := SourceNone
	if len(envKeys) > 0 {
		src = SourceEnv
	}
	s.set(envKeys, src)
	if path != "" {
		s.Reload()
	}
	return s, nil
}

// Collectors are the store's metrics, to register once.
func (s *Store) Collectors() []prometheus.Collector {
	return []prometheus.Collector{s.info, s.errs, s.reload}
}

// Keys returns a copy of the current list (never nil).
func (s *Store) Keys() []string { return append([]string{}, s.cur.Load().keys...) }

// Has reports whether key is on.
func (s *Store) Has(key string) bool {
	for _, k := range s.cur.Load().keys {
		if k == key {
			return true
		}
	}
	return false
}

// SHA is the short hash of the served list; Source is where it came from.
func (s *Store) SHA() string    { return s.cur.Load().sha }
func (s *Store) Source() string { return s.cur.Load().source }

func (s *Store) set(keys []string, source string) {
	sum := sha256.Sum256([]byte(strings.Join(keys, ",")))
	s.cur.Store(&snapshot{keys: append([]string{}, keys...), sha: hex.EncodeToString(sum[:])[:12], source: source})
	// One series at 1: the old sha is removed, not left at 1 beside the new one.
	s.info.Reset()
	s.info.WithLabelValues(s.cur.Load().sha, source).Set(1)
	s.reload.SetToCurrentTime()
}

// Reload reads the file once. A rejected read keeps the last good list, counts an error and logs; a good read that differs swaps the list and logs it.
func (s *Store) Reload() {
	if s.path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	var keys []string
	if err == nil {
		keys, err = ParseFile(b)
	}
	if err != nil {
		s.errs.Inc()
		c := s.cur.Load()
		s.log.Error("live keys: file rejected, keeping the last good list", "path", s.path, "err", err.Error(), "keys", strings.Join(c.keys, ","), "sha", c.sha, "source", c.source)
		return
	}
	c := s.cur.Load()
	if c.source == SourceFile && strings.Join(c.keys, ",") == strings.Join(keys, ",") {
		return
	}
	s.set(keys, SourceFile)
	s.log.Info("live keys: list changed", "keys", strings.Join(keys, ","), "sha", s.cur.Load().sha, "was", strings.Join(c.keys, ","), "was_source", c.source)
}

// Run polls until stop is closed.
func (s *Store) Run(stop <-chan struct{}, every time.Duration) {
	if s.path == "" {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.Reload()
		}
	}
}
