package livekeys

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestParseFile(t *testing.T) {
	ok := map[string][]string{
		"design_preview=a,b\n":                  {"a", "b"},
		"design_preview=\n":                     {},
		"# note\n\ndesign_preview = a, b ,a,\n": {"a", "b"},
		"design_preview=theme_switch,now_v2":    {"theme_switch", "now_v2"},
		"design_preview=ios_elevation\r\n":      {"ios_elevation"},
	}
	for in, want := range ok {
		got, err := ParseFile([]byte(in))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("ParseFile(%q) = %#v, %v; want %#v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "\n  \n", "# only a comment\n", "other=a\n", "design_preview=A\n", "design_preview=a-b\n", "design_preview=a b\n", "design_preview=a\ndesign_preview=b\n", "a,b\n"} {
		if got, err := ParseFile([]byte(in)); err == nil {
			t.Errorf("ParseFile(%q) = %#v, want an error", in, got)
		}
	}
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBootAndReload(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")

	// No file yet: env is the list, source env.
	s, err := New(p, "a,b", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Keys(); !reflect.DeepEqual(got, []string{"a", "b"}) || s.Source() != SourceEnv {
		t.Fatalf("boot without file: %v %s", got, s.Source())
	}
	if n := testutil.ToFloat64(s.errs); n != 1 {
		t.Fatalf("a missing file at boot counts an error, got %v", n)
	}

	// A good file wins over env.
	write(t, p, "design_preview=b,c\n")
	s.Reload()
	if got := s.Keys(); !reflect.DeepEqual(got, []string{"b", "c"}) || s.Source() != SourceFile {
		t.Fatalf("after good file: %v %s", got, s.Source())
	}
	sha := s.SHA()
	if !s.Has("c") || s.Has("a") {
		t.Fatal("Has")
	}

	// Every kind of bad file keeps the last good list and counts.
	before := testutil.ToFloat64(s.errs)
	for _, body := range []string{"", "design_preview=Bad Key\n", "nonsense\n", "# x\n"} {
		write(t, p, body)
		s.Reload()
		if got := s.Keys(); !reflect.DeepEqual(got, []string{"b", "c"}) || s.SHA() != sha {
			t.Fatalf("bad file %q changed the list to %v", body, got)
		}
	}
	if os.Remove(p) != nil {
		t.Fatal("remove")
	}
	s.Reload()
	if got := s.Keys(); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Fatalf("missing file changed the list to %v", got)
	}
	if n := testutil.ToFloat64(s.errs) - before; n != 5 {
		t.Fatalf("errors counted = %v, want 5", n)
	}

	// An empty value is valid: every key off.
	write(t, p, "design_preview=\n")
	s.Reload()
	if got := s.Keys(); len(got) != 0 || s.Source() != SourceFile {
		t.Fatalf("empty value: %v %s", got, s.Source())
	}

	// One series on the info gauge, for the current sha.
	if n := testutil.CollectAndCount(s.info); n != 1 {
		t.Fatalf("info series = %d, want 1", n)
	}
}

func TestBootNoFileNoEnvAndBadEnv(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "nope"), "", nil)
	if err != nil || len(s.Keys()) != 0 || s.Source() != SourceInvalid || s.Ready() {
		t.Fatalf("file absent and no env: nothing to serve, so not ready: %v %v %s", s.Keys(), err, s.Source())
	}
	if s, err := New("", "", nil); err != nil || s.Source() != SourceNone || !s.Ready() {
		t.Fatalf("no file configured, no env: empty list, ready: %v %v", s.Source(), err)
	}
	if _, err := New("", "good,Bad-Key", nil); err == nil || !strings.Contains(err.Error(), "RIVOLT_DESIGN_PREVIEW") {
		t.Fatalf("a bad env list must be an error, got %v", err)
	}
}

// Following kubelet's atomic swap: the file path is a symlink chain whose target is replaced.
func TestFollowsSymlinkSwap(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "v1"), "design_preview=a\n")
	write(t, filepath.Join(dir, "v2"), "design_preview=b\n")
	link := filepath.Join(dir, "..data")
	if err := os.Symlink("v1", link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data", filepath.Join(dir, "keys")); err != nil {
		t.Fatal(err)
	}
	s, _ := New(filepath.Join(dir, "keys"), "", nil)
	if got := s.Keys(); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatal(got)
	}
	tmp := filepath.Join(dir, "..tmp")
	_ = os.Symlink("v2", tmp)
	if err := os.Rename(tmp, link); err != nil {
		t.Fatal(err)
	}
	s.Reload()
	if got := s.Keys(); !reflect.DeepEqual(got, []string{"b"}) {
		t.Fatal(got)
	}
}

// N readers while the file flips between lists: every read sees one whole list, never a mix, and -race stays quiet.
func TestConcurrentReaders(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")
	write(t, p, "design_preview=a,b\n")
	s, _ := New(p, "", nil)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				k := s.Keys()
				if !(len(k) == 2 && k[0] == "a" && k[1] == "b") && !(len(k) == 2 && k[0] == "c" && k[1] == "d") {
					t.Errorf("torn list %v", k)
					return
				}
				_ = s.Has("a")
			}
		}()
	}
	for i := 0; i < 300; i++ {
		if i%2 == 0 {
			write(t, p+".tmp", "design_preview=c,d\n")
		} else {
			write(t, p+".tmp", "design_preview=a,b\n")
		}
		_ = os.Rename(p+".tmp", p)
		s.Reload()
	}
	close(stop)
	wg.Wait()
}

func info(t *testing.T, s *Store) (sha, source string) {
	t.Helper()
	ch := make(chan prometheus.Metric, 4)
	s.info.Collect(ch)
	close(ch)
	var m dto.Metric
	n := 0
	for x := range ch {
		n++
		_ = x.Write(&m)
		for _, l := range m.Label {
			switch l.GetName() {
			case "sha":
				sha = l.GetValue()
			case "source":
				source = l.GetValue()
			}
		}
	}
	if n != 1 {
		t.Fatalf("info series = %d", n)
	}
	return
}

// A pod that starts with a present but invalid file serves nothing as if intended: source invalid, not ready, even with a valid env.
// The env is for an ABSENT file only. The next poll with a good file recovers it.
func TestBootPresentInvalidFileIsNotReady(t *testing.T) {
	for _, env := range []string{"", "x,y"} {
		p := filepath.Join(t.TempDir(), "keys")
		write(t, p, "design_preview=now v2\n")
		s, err := New(p, env, nil)
		if err != nil {
			t.Fatal(err)
		}
		if s.Ready() || s.Source() != SourceInvalid || len(s.Keys()) != 0 {
			t.Fatalf("env %q: present invalid file at boot: ready=%v source=%s keys=%v", env, s.Ready(), s.Source(), s.Keys())
		}
		if _, src := info(t, s); src != SourceInvalid {
			t.Fatalf("info source = %s", src)
		}
		if testutil.ToFloat64(s.rejected) != 1 {
			t.Fatal("rejected gauge should be 1")
		}
		// Fixed: ready again within one poll.
		write(t, p, "design_preview=a,b\n")
		s.Reload()
		if !s.Ready() || s.Source() != SourceFile || !reflect.DeepEqual(s.Keys(), []string{"a", "b"}) {
			t.Fatalf("after the fix: ready=%v source=%s keys=%v", s.Ready(), s.Source(), s.Keys())
		}
		if testutil.ToFloat64(s.rejected) != 0 {
			t.Fatal("rejected gauge should clear")
		}
		// And broken again while running: the last good list stays, still ready.
		write(t, p, "")
		s.Reload()
		if !s.Ready() || s.Source() != SourceFile || !reflect.DeepEqual(s.Keys(), []string{"a", "b"}) {
			t.Fatalf("bad while running: ready=%v source=%s keys=%v", s.Ready(), s.Source(), s.Keys())
		}
	}
}

// Absent file with a valid env: the env list, ready. A bad file arriving later does not take a running pod out of service.
func TestAbsentFileUsesEnvAndLaterBadFileKeepsIt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")
	s, _ := New(p, "x,y", nil)
	if !s.Ready() || s.Source() != SourceEnv || !reflect.DeepEqual(s.Keys(), []string{"x", "y"}) {
		t.Fatalf("absent + env: %v %s %v", s.Ready(), s.Source(), s.Keys())
	}
	write(t, p, "design_preview=Bad\n")
	s.Reload()
	if !s.Ready() || s.Source() != SourceEnv || !reflect.DeepEqual(s.Keys(), []string{"x", "y"}) {
		t.Fatalf("bad file after an env boot: %v %s %v", s.Ready(), s.Source(), s.Keys())
	}
}

// A bad content is counted and logged once, however many polls see it; a different bad content counts again; a good read resets.
func TestBadContentCountedOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys")
	write(t, p, "design_preview=a\n")
	s, _ := New(p, "", nil)
	write(t, p, "design_preview=A\n")
	for i := 0; i < 20; i++ {
		s.Reload()
	}
	if n := testutil.ToFloat64(s.errs); n != 1 {
		t.Fatalf("20 polls of one bad content counted %v, want 1", n)
	}
	if testutil.ToFloat64(s.rejected) != 1 || !reflect.DeepEqual(s.Keys(), []string{"a"}) {
		t.Fatal("gauge 1 and the last good list stays")
	}
	write(t, p, "design_preview=B\n")
	s.Reload()
	s.Reload()
	if n := testutil.ToFloat64(s.errs); n != 2 {
		t.Fatalf("a different bad content counts again: %v", n)
	}
	write(t, p, "design_preview=a\n")
	s.Reload()
	write(t, p, "design_preview=B\n")
	s.Reload()
	if n := testutil.ToFloat64(s.errs); n != 3 {
		t.Fatalf("the same bad content after a good read counts again: %v", n)
	}
}
