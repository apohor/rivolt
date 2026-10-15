package main

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
)

// Every --ids entry that won't be re-priced is named, with why.
func TestRepriceUnaccounted(t *testing.T) {
	ids := []string{"a", "b", "c", "d"}
	got := repriceUnaccounted(ids, map[string]string{
		"a": "re-price",
		"b": "skipped: no DCFC station within range",
		"d": "re-price",
	})
	if len(got) != 2 || !strings.HasPrefix(got[0], "b  skipped: no DCFC") || !strings.HasPrefix(got[1], "c  not selected") {
		t.Fatalf("got %q", got)
	}
	if got := repriceUnaccounted(ids[:1], map[string]string{"a": "re-price"}); len(got) != 0 {
		t.Fatalf("all re-priced: %q", got)
	}
	if got := repriceUnaccounted(nil, nil); len(got) != 0 {
		t.Fatalf("no --ids: %q", got)
	}
}

// The backup round-trips: header, then each row's id, user and JSON.
func TestRepriceBackupCSV(t *testing.T) {
	rows := []repriceBackupRow{
		{ChargeID: "c1", UserID: "u1", Row: []byte(`{"cost": 8.34, "network": null, "note": "a,b \"q\""}`)},
		{ChargeID: "c2", UserID: "u2", Row: []byte(`{"cost": 0}`)},
	}
	var buf bytes.Buffer
	if err := writeRepriceCSV(&buf, rows); err != nil {
		t.Fatal(err)
	}
	recs, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || strings.Join(recs[0], ",") != "charge_id,user_id,row_json" {
		t.Fatalf("records: %q", recs)
	}
	for i, r := range rows {
		if recs[i+1][0] != r.ChargeID || recs[i+1][1] != r.UserID || recs[i+1][2] != string(r.Row) {
			t.Errorf("row %d: %q", i, recs[i+1])
		}
	}
}
