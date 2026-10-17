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

// A --backup file reads back as written; anything else is refused.
func TestReadRepriceCSV(t *testing.T) {
	rows := []repriceBackupRow{
		{ChargeID: "36e96529-40ad-49e2-b2e4-db42cefc9447", UserID: "206f57ae-1047-565a-a5f7-b49718836f74", Row: []byte(`{"cost": 8.338, "note": "a,b \"q\""}`)},
	}
	var buf bytes.Buffer
	if err := writeRepriceCSV(&buf, rows); err != nil {
		t.Fatal(err)
	}
	got, err := readRepriceCSV(&buf)
	if err != nil || len(got) != 1 || got[0].ChargeID != rows[0].ChargeID || string(got[0].Row) != string(rows[0].Row) {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	for _, bad := range []string{
		"charge_id,user_id\n",
		"a,b,c\n1,2,{}\n",
		"charge_id,user_id,row_json\nnot-a-uuid,206f57ae-1047-565a-a5f7-b49718836f74,{}\n",
	} {
		if _, err := readRepriceCSV(strings.NewReader(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
