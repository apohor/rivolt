package api

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"
)

func gz(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Plain and gzip bodies both come out as the same text; gzip is recognised
// by its magic bytes, not by a header the client may or may not send.
func TestRestoreSourceDetectsGzip(t *testing.T) {
	const doc = `{"drives":[],"charges":[],"samples":[]}`
	for name, body := range map[string][]byte{"plain": []byte(doc), "gzip": gz(t, doc)} {
		src, closeFn, err := restoreSource(bytes.NewReader(body), 1<<20)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got, err := io.ReadAll(src)
		closeFn()
		if err != nil || string(got) != doc {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	// Corrupt gzip (magic bytes, garbage after) is an error, not a hang.
	if src, _, err := restoreSource(bytes.NewReader([]byte{0x1f, 0x8b, 0, 1, 2}), 1<<20); err == nil {
		if _, err := io.ReadAll(src); err == nil {
			t.Error("corrupt gzip read without error")
		}
	}
}

// The cap counts decompressed bytes, so a small gzip that unpacks huge is stopped.
func TestRestoreSourceCapsDecompressedSize(t *testing.T) {
	bomb := gz(t, strings.Repeat("a", 1<<20)) // ~1 KB on the wire, 1 MiB unpacked
	if len(bomb) > 4<<10 {
		t.Fatalf("test input is not small: %d bytes", len(bomb))
	}
	src, closeFn, err := restoreSource(bytes.NewReader(bomb), 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if _, err := io.ReadAll(src); !errors.Is(err, errRestoreTooBig) {
		t.Errorf("read error = %v, want errRestoreTooBig", err)
	}
}
