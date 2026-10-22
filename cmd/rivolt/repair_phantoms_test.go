package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/repair/phantom"
)

func TestParseIDFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ids")
	a := "a75c3ecc-e384-4205-8f61-b1098e2e7374"
	_ = os.WriteFile(p, []byte("# audited\n"+a+" | 01-000000007 | x\n\n"+a+"\nf5b4c562-c391-4e5c-96e1-de7e4fd1cc2f,note\n"), 0o600)
	ids, err := parseIDFile(p)
	if err != nil || len(ids) != 2 {
		t.Fatalf("%v %v", ids, err)
	}
	_ = os.WriteFile(p, []byte("not-a-uuid\n"), 0o600)
	if _, err := parseIDFile(p); err == nil {
		t.Fatal("bad id must error")
	}
}

func TestPhantomCSVRoundTripAndExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "b.csv")
	c, err := newPhantomCSV(p)
	if err != nil {
		t.Fatal(err)
	}
	batch := uuid.New()
	cid, uid := uuid.New(), uuid.New()
	in := []phantom.BackupRow{{Kind: "charge", ChargeID: cid, UserID: uid, Row: []byte(`{"id":"x","note":"a,\"b\""}`)}}
	if err := c.sink(batch)(in); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := readPhantomCSV(p)
	if err != nil || len(out) != 1 || out[0].ChargeID != cid || string(out[0].Row) != string(in[0].Row) {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := newPhantomCSV(p); err == nil {
		t.Fatal("existing backup must not be overwritten")
	}
}
