package archive

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/crypto"
)

func seal(t *testing.T, key, aad, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewEncryptWriter(&buf, key, aad)
	if err != nil {
		t.Fatal(err)
	}
	// Uneven writes, to cross chunk boundaries mid-write.
	for p := plain; len(p) > 0; {
		n := 7001
		if n > len(p) {
			n = len(p)
		}
		if _, err := w.Write(p[:n]); err != nil {
			t.Fatal(err)
		}
		p = p[n:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func open(key, aad, obj []byte) ([]byte, error) {
	r, err := NewDecryptReader(bytes.NewReader(obj), key, aad)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

func TestStreamRoundTrip(t *testing.T) {
	key, _ := NewDataKey()
	aad := []byte("rivolt-archive/v1|u|v|2026-05")
	for _, n := range []int{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 3 * ChunkSize, 3*ChunkSize + 12345} {
		plain := make([]byte, n)
		_, _ = rand.Read(plain)
		obj := seal(t, key, aad, plain)
		chunks := n/ChunkSize + 1
		if n > 0 && n%ChunkSize == 0 {
			chunks = n / ChunkSize
		}
		if want := headerSize + n + chunks*tagSize; len(obj) != want {
			t.Errorf("%d bytes: object %d bytes, want %d", n, len(obj), want)
		}
		got, err := open(key, aad, obj)
		if err != nil || !bytes.Equal(got, plain) {
			t.Errorf("%d bytes: round trip %d bytes, %v", n, len(got), err)
		}
	}
}

func TestStreamRejectsTampering(t *testing.T) {
	key, _ := NewDataKey()
	aad := []byte("rivolt-archive/v1|u|v|2026-05")
	plain := make([]byte, 2*ChunkSize+500)
	_, _ = rand.Read(plain)
	obj := seal(t, key, aad, plain)
	other, _ := NewDataKey()
	first := headerSize + ChunkSize + tagSize // end of chunk 0
	cases := map[string]func() ([]byte, []byte, []byte){
		"wrong key":       func() ([]byte, []byte, []byte) { return other, aad, obj },
		"wrong aad":       func() ([]byte, []byte, []byte) { return key, []byte("rivolt-archive/v1|other|v|2026-05"), obj },
		"flipped bit":     func() ([]byte, []byte, []byte) { o := bytes.Clone(obj); o[first+10] ^= 1; return key, aad, o },
		"final chunk cut": func() ([]byte, []byte, []byte) { return key, aad, obj[:2*(ChunkSize+tagSize)+headerSize] },
		"one chunk only":  func() ([]byte, []byte, []byte) { return key, aad, obj[:first] },
		"last byte gone":  func() ([]byte, []byte, []byte) { return key, aad, obj[:len(obj)-1] },
		"header only":     func() ([]byte, []byte, []byte) { return key, aad, obj[:headerSize] },
		"bad magic":       func() ([]byte, []byte, []byte) { o := bytes.Clone(obj); o[0] = 'X'; return key, aad, o },
		"chunks reordered": func() ([]byte, []byte, []byte) {
			o := bytes.Clone(obj)
			c0 := bytes.Clone(o[headerSize:first])
			copy(o[headerSize:first], o[first:first+ChunkSize+tagSize])
			copy(o[first:first+ChunkSize+tagSize], c0)
			return key, aad, o
		},
	}
	for name, mk := range cases {
		k, a, o := mk()
		if got, err := open(k, a, o); !errors.Is(err, ErrCorrupt) {
			t.Errorf("%s: %d bytes, err %v; want ErrCorrupt", name, len(got), err)
		}
	}
	// Two encryptions of the same plaintext differ (random nonce prefix).
	if bytes.Equal(seal(t, key, aad, plain), obj) {
		t.Error("nonce prefix reused")
	}
}

func TestDataKeySealing(t *testing.T) {
	ctx := context.Background()
	s, err := crypto.NewEnvSealer("v1", bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	uid, other := uuid.New(), uuid.New()
	key, _ := NewDataKey()
	sealed, err := SealDataKey(ctx, s, uid, key)
	if err != nil || bytes.Contains(sealed, key) {
		t.Fatalf("seal: %v (key in the clear: %v)", err, bytes.Contains(sealed, key))
	}
	if got, err := OpenDataKey(ctx, s, uid, sealed); err != nil || !bytes.Equal(got, key) {
		t.Errorf("open: %v", err)
	}
	if _, err := OpenDataKey(ctx, s, other, sealed); err == nil {
		t.Error("another user's id opened the key")
	}
	if _, err := SealDataKey(ctx, crypto.NoopSealer{}, uid, key); !errors.Is(err, ErrNoKEK) {
		t.Errorf("noop sealer: %v, want ErrNoKEK", err)
	}
	if !bytes.Contains(ObjectAAD(uid, other, "2026-05"), []byte(uid.String())) {
		t.Error("ObjectAAD")
	}
}
