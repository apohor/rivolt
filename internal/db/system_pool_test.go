package db

import "testing"

func TestSystemPoolSize(t *testing.T) {
	env := func(maxConns, requestURL string) func(string) string {
		return func(k string) string {
			switch k {
			case "RIVOLT_DB_MAX_CONNS":
				return maxConns
			case "RIVOLT_REQUEST_DATABASE_URL":
				return requestURL
			}
			t.Fatalf("read %s", k)
			return ""
		}
	}
	const url = "postgres://app@db/rivolt"
	for _, c := range []struct {
		name, maxConns, requestURL string
		want                       int
		reasonHas                  string
	}{
		{"request pool off: today's 20, since the system pool serves requests too", "", "", 20, "request pool off"},
		{"request pool on: 10, requests have their own pool", "", url, 10, "request pool on"},
		{"blank request URL counts as off", "", "  ", 20, "request pool off"},
		{"explicit value wins with the pool off", "6", "", 6, "RIVOLT_DB_MAX_CONNS"},
		{"explicit value wins with the pool on", " 12 ", url, 12, "RIVOLT_DB_MAX_CONNS"},
		{"upper bound", "100", url, 100, "RIVOLT_DB_MAX_CONNS"},
		{"zero is not valid: the mode's default (off)", "0", "", 20, "request pool off"},
		{"too large: the mode's default (on)", "101", url, 10, "request pool on"},
		{"negative", "-3", "", 20, "request pool off"},
		{"not a number", "many", url, 10, "request pool on"},
	} {
		got, reason := SystemPoolSize(env(c.maxConns, c.requestURL))
		if got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got, c.want)
		}
		if !contains(reason, c.reasonHas) {
			t.Errorf("%s: reason %q, want it to mention %q", c.name, reason, c.reasonHas)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
