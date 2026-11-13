package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClassifyOpenCharge(t *testing.T) {
	const id = "0123456789abcdef"
	cases := []struct {
		name  string
		n     chargeNow
		since int64
		level string
		has   string
	}{
		{"still charging", chargeNow{Rows: 1, State: "charging_active", AgeSeconds: 30}, 600, "ok", "one row"},
		{"gone", chargeNow{Missing: true}, 600, "fail", "gone"},
		{"split", chargeNow{Rows: 2, RowIDs: "01234567,89abcdef", State: "charging_active", AgeSeconds: 5}, 600, "fail", "2 rows"},
		{"stalled", chargeNow{Rows: 1, State: "charging_active", AgeSeconds: 700}, 600, "fail", "stalled"},
		{"ended before the roll", chargeNow{Rows: 1, State: "charging_done", AgeSeconds: 4000}, 600, "ok", "one row"},
		{"ended inside the roll", chargeNow{Rows: 1, State: "charging_done", AgeSeconds: 120}, 600, "warn", "inside the roll window"},
		{"ended, roll unknown", chargeNow{Rows: 1, State: "charging_done", AgeSeconds: 120}, 0, "ok", "one row"},
	}
	for _, c := range cases {
		level, msg := classifyOpenCharge(id, c.n, c.since)
		if level != c.level || !strings.Contains(msg, c.has) {
			t.Errorf("%s: got %s %q, want %s containing %q", c.name, level, msg, c.level, c.has)
		}
	}
}

func TestSecondsSinceRollStart(t *testing.T) {
	now := time.Unix(10_000, 0)
	if got := secondsSinceRollStart(9_000, time.Unix(9_500, 0), now); got != 1000 {
		t.Errorf("explicit roll-from: %d, want 1000", got)
	}
	if got := secondsSinceRollStart(0, time.Unix(9_500, 0), now); got != 500 {
		t.Errorf("default to the snapshot time: %d, want 500", got)
	}
	if got := secondsSinceRollStart(0, time.Time{}, now); got != 0 {
		t.Errorf("no time at all: %d, want 0", got)
	}
}

func TestOpenChargesUsageExits2(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"snapshot", "extra"}, {"verify", "--nope"}} {
		if code := runOpenCharges(args, io.Discard, io.Discard); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}

func TestOpenChargesNeedsDSN(t *testing.T) {
	t.Setenv("RIVOLT_VERIFY_DATABASE_URL", "")
	var errb bytes.Buffer
	if code := runOpenCharges([]string{"snapshot"}, io.Discard, &errb); code != 1 || !strings.Contains(errb.String(), "RIVOLT_VERIFY_DATABASE_URL") {
		t.Errorf("exit %d, stderr %q", code, errb.String())
	}
}

func TestPutConfigMapCreatesThenReplaces(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Error("missing bearer token")
		}
		var cm struct {
			Data map[string]string `json:"data"`
		}
		_ = json.NewDecoder(r.Body).Decode(&cm)
		if cm.Data[openChargesKey] != "{}" {
			t.Errorf("data = %v", cm.Data)
		}
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	k := &kubeAPI{base: srv.URL, token: "tok", ns: "rivolt", hc: srv.Client()}
	if err := k.putConfigMap(context.Background(), "oc", map[string]string{openChargesKey: "{}"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /api/v1/namespaces/rivolt/configmaps", "PUT /api/v1/namespaces/rivolt/configmaps/oc"}
	if strings.Join(methods, "|") != strings.Join(want, "|") {
		t.Errorf("calls = %v, want %v", methods, want)
	}
}

func TestPutConfigMapReportsForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	k := &kubeAPI{base: srv.URL, token: "t", ns: "n", hc: srv.Client()}
	if err := k.putConfigMap(context.Background(), "oc", nil); err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v", err)
	}
}
