package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"

	rivoltcrypto "github.com/apohor/rivolt/internal/crypto"
)

func kek(id string, fill byte) string {
	return id + ":" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

// RIVOLT_KEK_ROTATION holds the retired keys' values. After a rotation, rows
// sealed under the old key still open (through buildSealer, the path boot takes),
// new rows are sealed under the new key, and a bad list refuses to boot.
func TestBuildSealerRotationKeepsOldRowsReadable(t *testing.T) {
	t.Setenv("RIVOLT_ALLOW_NOOP_SEALER", "")
	ctx := context.Background()
	uid := uuid.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Before the rotation: key A.
	t.Setenv("RIVOLT_KEK", kek("k1", 1))
	t.Setenv("RIVOLT_KEK_ROTATION", "")
	before, err := buildSealer(logger)
	if err != nil {
		t.Fatal(err)
	}
	old, err := before.Seal(ctx, uid, []byte("rivian-session"))
	if err != nil {
		t.Fatal(err)
	}

	// After: key B is primary, key A is retired (given by value, as documented).
	t.Setenv("RIVOLT_KEK", kek("k2", 2))
	t.Setenv("RIVOLT_KEK_ROTATION", kek("k1", 1))
	after, err := buildSealer(logger)
	if err != nil {
		t.Fatalf("boot after rotation: %v", err)
	}
	got, err := after.Open(ctx, uid, old)
	if err != nil || string(got) != "rivian-session" {
		t.Fatalf("a row sealed under the retired key no longer opens: %q %v", got, err)
	}
	if after.KEKID() != "k2" {
		t.Errorf("new rows sealed under %q, want k2", after.KEKID())
	}
	fresh, _ := after.Seal(ctx, uid, []byte("new"))
	if _, err := before.Open(ctx, uid, fresh); err == nil {
		t.Error("the old key opened a row sealed under the new one")
	}

	// Without the retired key the old row cannot be read: that is what the
	// list is for.
	t.Setenv("RIVOLT_KEK_ROTATION", "")
	alone, _ := buildSealer(logger)
	if _, err := alone.Open(ctx, uid, old); err == nil {
		t.Error("a row opened without its key")
	}
}

func TestBuildSealerRefusesABadRotationList(t *testing.T) {
	t.Setenv("RIVOLT_ALLOW_NOOP_SEALER", "")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv("RIVOLT_KEK", kek("k2", 2))
	for name, rot := range map[string]string{
		"a variable name, as it used to be read": "OLD_KEK_VAR",
		"not base64":                             "k1:%%%not-base64",
		"wrong length":                           "k1:" + base64.StdEncoding.EncodeToString([]byte("short")),
		"no id":                                  ":" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)),
		"a good one then a bad one":              kek("k1", 1) + ",junk",
		"two keys under one id":                  kek("k1", 1) + "," + kek("k1", 9),
	} {
		t.Setenv("RIVOLT_KEK_ROTATION", rot)
		if _, err := buildSealer(logger); err == nil {
			t.Errorf("%s: booted", name)
		}
	}
	// Blank entries and the primary listed again are fine.
	t.Setenv("RIVOLT_KEK_ROTATION", " , "+kek("k2", 2)+" ,"+kek("k1", 1))
	s, err := buildSealer(logger)
	if err != nil {
		t.Fatalf("tolerated list: %v", err)
	}
	if es, ok := s.(*rivoltcrypto.EnvSealer); !ok {
		t.Fatalf("sealer %T", s)
	} else if p, r := es.KEKIDs(); p != "k2" || strings.Join(r, ",") != "k1" {
		t.Errorf("ids primary %q retired %v", p, r)
	}
	// An unset primary is still fatal.
	t.Setenv("RIVOLT_KEK", "")
	if _, err := buildSealer(logger); err == nil {
		t.Error("booted with no primary KEK")
	}
}
