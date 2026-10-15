package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseIDFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "ids")
	a := "a75c3ecc-e384-4205-8f61-b1098e2e7374"
	os.WriteFile(p, []byte("# audited\n"+a+" | 01-241831670 | x\n\n"+a+"\nf5b4c562-c391-4e5c-96e1-de7e4fd1cc2f,note\n"), 0o600)
	ids, err := parseIDFile(p)
	if err != nil || len(ids) != 2 {
		t.Fatalf("%v %v", ids, err)
	}
	os.WriteFile(p, []byte("not-a-uuid\n"), 0o600)
	if _, err := parseIDFile(p); err == nil {
		t.Fatal("bad id must error")
	}
}

func TestWritePhantomBackupExclusive(t *testing.T) {
	p := filepath.Join(t.TempDir(), "b.csv")
	if err := writePhantomBackup(p, [16]byte{1}, nil); err != nil {
		t.Fatal(err)
	}
	if err := writePhantomBackup(p, [16]byte{1}, nil); err == nil {
		t.Fatal("existing backup must not be overwritten")
	}
}
