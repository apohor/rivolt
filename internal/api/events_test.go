package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type fakeSink struct {
	anon string
	uid  *uuid.UUID
	evs  []storedEvent
	n    int
}

func (f *fakeSink) Insert(_ context.Context, anon string, uid *uuid.UUID, evs []storedEvent) error {
	f.anon, f.uid, f.evs, f.n = anon, uid, evs, f.n+1
	return nil
}

func postEvents(h http.Handler, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/events", strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	maxBodyBytes(eventsMaxBody)(h).ServeHTTP(rec, req)
	return rec
}

const anon = "abcdefghijklmnop1234"

func TestEvents_StoresAllowlistedOnly(t *testing.T) {
	s := &fakeSink{}
	h := handleEvents(s)
	body := `{"anon_id":"` + anon + `","events":[
		{"name":"receipt_open","props":{"source":"billed","email":"a@b.c","lat":41.2}},
		{"name":"rivian_connect_done","props":{"vehicle_count":2,"vin":"X"}},
		{"name":"signup_start","props":{"method":"password"}}]}`
	rec := postEvents(h, body, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code %d: %s", rec.Code, rec.Body)
	}
	if len(s.evs) != 3 || s.anon != anon || s.uid != nil {
		t.Fatalf("stored %+v anon=%s uid=%v", s.evs, s.anon, s.uid)
	}
	if got := s.evs[0].Props; len(got) != 1 || got["source"] != "billed" {
		t.Fatalf("props not filtered: %v", got)
	}
	if got := s.evs[1].Props; len(got) != 1 || got["vehicle_count"] != int64(2) {
		t.Fatalf("props: %v", got)
	}
	if len(s.evs[2].Props) != 0 {
		t.Fatalf("invalid enum value kept: %v", s.evs[2].Props)
	}
}

func TestEvents_Rejections(t *testing.T) {
	cases := map[string]string{
		"unknown name": `{"anon_id":"` + anon + `","events":[{"name":"page_view"}]}`,
		"bad anon":     `{"anon_id":"short","events":[{"name":"landing_view"}]}`,
		"empty":        `{"anon_id":"` + anon + `","events":[]}`,
		"not json":     `nope`,
		"too big":      `{"anon_id":"` + anon + `","events":[{"name":"landing_view","props":{"utm_source":"` + strings.Repeat("a", 3000) + `"}}]}`,
	}
	for name, body := range cases {
		s := &fakeSink{}
		rec := postEvents(handleEvents(s), body, nil)
		if rec.Code != http.StatusBadRequest || s.n != 0 {
			t.Errorf("%s: code %d stored %d", name, rec.Code, s.n)
		}
	}
	over := `{"anon_id":"` + anon + `","events":[` + strings.TrimSuffix(strings.Repeat(`{"name":"demo_tab"},`, 21), ",") + `]}`
	if rec := postEvents(handleEvents(&fakeSink{}), over, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("21 events: %d", rec.Code)
	}
}

func TestEvents_DNTStoresNothing(t *testing.T) {
	s := &fakeSink{}
	body := `{"anon_id":"` + anon + `","events":[{"name":"landing_view"}]}`
	for _, hdr := range []map[string]string{{"DNT": "1"}, {"Sec-GPC": "1"}} {
		if rec := postEvents(handleEvents(s), body, hdr); rec.Code != http.StatusNoContent || s.n != 0 {
			t.Fatalf("%v: code %d stored %d", hdr, rec.Code, s.n)
		}
	}
}

func TestEvents_FlagOffByDefault(t *testing.T) {
	t.Setenv("RIVOLT_EVENTS", "")
	if eventsEnabled() {
		t.Fatal("events must default off")
	}
	t.Setenv("RIVOLT_EVENTS", "true")
	if !eventsEnabled() {
		t.Fatal("RIVOLT_EVENTS=true should enable")
	}
}
