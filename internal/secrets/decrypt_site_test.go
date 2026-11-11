package secrets

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The observer hears the site a caller set, "" when it set none, and nothing once removed.
func TestSessionDecryptObserver(t *testing.T) {
	var got []string
	SetSessionDecryptObserver(func(site string) { got = append(got, site) })
	t.Cleanup(func() { SetSessionDecryptObserver(nil) })
	observeSessionDecrypt(WithSite(context.Background(), "boot_sweep"))
	observeSessionDecrypt(context.Background())
	SetSessionDecryptObserver(nil)
	observeSessionDecrypt(WithSite(context.Background(), "sync"))
	if len(got) != 2 || got[0] != "boot_sweep" || got[1] != "" {
		t.Fatalf("observer heard %q, want [boot_sweep \"\"]", got)
	}
}

// Every LoadRivianSession call outside tests names its site with WithSite, so rivolt_rivian_session_decrypts_total says where a decrypt came
// from; a new caller that forgets lands in "other" and fails here until it is labelled (or listed, with the reason it is not yet).
func TestLoadRivianSessionCallersNameTheirSite(t *testing.T) {
	// Not labelled yet: vehicle_discovery.go is backend's M4-2b (#1722) and api/vehicles.go its /api/vehicles-from-Postgres track; both are
	// left alone until those merge, and count as "other" meanwhile.
	pending := map[string]bool{"cmd/rivolt/vehicle_discovery.go": true, "internal/api/vehicles.go": true}
	call := regexp.MustCompile(`LoadRivianSession\(`)
	root := filepath.Join("..", "..")
	var bad []string
	for _, dir := range []string{"cmd", "internal"} {
		filepath.Walk(filepath.Join(root, dir), func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			b, _ := os.ReadFile(p)
			for i, line := range strings.Split(string(b), "\n") {
				t := strings.TrimSpace(line)
				if !call.MatchString(line) || strings.HasPrefix(t, "//") || strings.HasPrefix(t, "func ") || strings.Contains(line, "WithSite(") {
					continue
				}
				if !pending[filepath.ToSlash(rel)] {
					bad = append(bad, filepath.ToSlash(rel)+":"+itoa(i+1))
				}
			}
			return nil
		})
	}
	if len(bad) > 0 {
		t.Fatalf("LoadRivianSession called without WithSite: %v", bad)
	}
}

func itoa(n int) string {
	var d []byte
	for ; n > 0; n /= 10 {
		d = append([]byte{byte('0' + n%10)}, d...)
	}
	return string(d)
}
