//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

func TestEngagementSettings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserVerified(ctx, pool, "eset", "eset@x.test", "eset", true)
	if err != nil {
		t.Fatal(err)
	}
	a := &engagementAPI{db: pool}
	do := func(h func(uuid.UUID, http.ResponseWriter, *http.Request), method, body string, imp bool) (int, engagementSettings) {
		req := httptest.NewRequest(method, "/api/settings/engagement", strings.NewReader(body))
		c := auth.WithUser(ctx, uid)
		if imp {
			c = auth.WithImpersonation(c, uuid.New(), uid)
		}
		rec := httptest.NewRecorder()
		h(uid, rec, req.WithContext(c))
		var out engagementSettings
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	code, s := do(a.get, "GET", "", false)
	if code != 200 || s.WeeklyRecap || s.TripRecap || s.PlugThresholdPct != 40 || s.PlugRemindAt != "20:00" || s.TimezoneStored || s.Timezone != "" {
		t.Fatalf("defaults: %d %+v", code, s)
	}
	code, s = do(a.put, "PUT", `{"timezone":"America/Chicago","weekly_recap":true,"plug_threshold_pct":30,"plug_remind_at":"21:15"}`, false)
	if code != 200 || !s.WeeklyRecap || s.PlugThresholdPct != 30 || s.PlugRemindAt != "21:15" || !s.TimezoneStored || s.Timezone != "America/Chicago" {
		t.Fatalf("saved: %d %+v", code, s)
	}
	// The trip recap is off until chosen, saved on its own, and keeps the other switches.
	if code, s = do(a.put, "PUT", `{"trip_recap":true}`, false); code != 200 || !s.TripRecap || !s.WeeklyRecap {
		t.Errorf("trip recap on: %d %+v", code, s)
	}
	if code, s = do(a.put, "PUT", `{"trip_recap":false,"weekly_recap":true}`, false); code != 200 || s.TripRecap || !s.WeeklyRecap {
		t.Errorf("trip recap off: %d %+v", code, s)
	}
	// A partial update keeps the rest.
	if code, s = do(a.put, "PUT", `{"weekly_recap":false}`, false); code != 200 || s.WeeklyRecap || s.PlugThresholdPct != 30 || s.Timezone != "America/Chicago" {
		t.Errorf("partial: %d %+v", code, s)
	}
	if code, s = do(a.get, "GET", "", false); code != 200 || s.PlugRemindAt != "21:15" {
		t.Errorf("reload: %d %+v", code, s)
	}
	for name, body := range map[string]string{
		"late reminder":  `{"plug_remind_at":"22:01"}`,
		"not a time":     `{"plug_remind_at":"evening"}`,
		"low threshold":  `{"plug_threshold_pct":2}`,
		"high threshold": `{"plug_threshold_pct":100}`,
		"unknown zone":   `{"timezone":"Mars/Base"}`,
		"local zone":     `{"timezone":"Local"}`,
		"empty zone":     `{"timezone":""}`,
		"not json":       `{`,
	} {
		if code, _ := do(a.put, "PUT", body, false); code != 400 {
			t.Errorf("%s: %d, want 400", name, code)
		}
	}
	if code, _ := do(a.put, "PUT", `{"weekly_recap":true}`, true); code != http.StatusForbidden {
		t.Errorf("view-as: %d, want 403", code)
	}
	if code, s = do(a.put, "PUT", `{"plug_remind_at":"22:00"}`, false); code != 200 || s.PlugRemindAt != "22:00" {
		t.Errorf("22:00 is the last allowed time: %d %+v", code, s)
	}
}
