package archive

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

var testCols = []string{"vehicle_id", "at", "lat", "shift_state", "raw"}

// object writes plain through objectWriter as the job does and returns the
// object and the plaintext hash.
func object(t *testing.T, key, aad []byte, plain string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	w, err := newObjectWriter(&buf, key, aad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(plain)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), w.plainSHA256()
}

// csvRows is a header and n rows as Postgres COPY CSV writes them: an empty
// unquoted field is NULL, and a field with a comma, quote or newline is
// quoted.
func csvRows(n int) string {
	var b strings.Builder
	b.WriteString(strings.Join(testCols, ",") + "\n")
	at := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		raw := ""
		if i%3 == 0 {
			raw = `"{""note"": ""a,b\nc""}"` // quoted newline: still one row
		}
		fmt.Fprintf(&b, "%s,%s,%.6f,%s,%s\n", "6f1c2a52-0000-4000-8000-000000000001",
			at.Add(time.Duration(i)*time.Second).Format(TimeLayout), 30.1+float64(i)/1e5, "P", raw)
	}
	return b.String()
}

func TestObjectRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	aad := ObjectAAD(uuid.New(), uuid.New(), "2026-05")
	plain := csvRows(50000) // several 64 KiB chunks after gzip
	obj, sum := object(t, key, aad, plain)
	want := sha256.Sum256([]byte(plain))
	if sum != hex.EncodeToString(want[:]) {
		t.Fatal("plaintext hash differs")
	}
	s, err := ReadStats(bytes.NewReader(obj), key, aad, testCols)
	if err != nil {
		t.Fatal(err)
	}
	if s.Rows != 50000 || s.FirstAt != "2026-05-01T00:00:00.000000Z" || s.LastAt != "2026-05-01T13:53:19.000000Z" || s.SHA256Plain != sum {
		t.Errorf("stats %+v", s)
	}
}

// The STREAM construction authenticates the end last: rows come out of a
// truncated or tampered object before the error does. ReadStats must still
// fail as a whole (DA, A2b review).
func TestReadStatsFailsOnLateCorruption(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	aad := ObjectAAD(uuid.New(), uuid.New(), "2026-05")
	obj, _ := object(t, key, aad, csvRows(50000))
	if len(obj) < 3*ChunkSize {
		t.Fatalf("object is %d bytes; the test needs several chunks", len(obj))
	}
	lastChunk := len(obj) - 10
	cases := map[string][]byte{
		"truncated at a chunk boundary": obj[:headerSize+2*sealedChunk],
		"last bytes cut":                obj[:len(obj)-1],
		"last chunk flipped":            flip(obj, lastChunk),
		"trailing bytes":                append(append([]byte{}, obj...), 0),
	}
	for name, bad := range cases {
		s, err := ReadStats(bytes.NewReader(bad), key, aad, testCols)
		if err == nil {
			t.Errorf("%s: no error", name)
		}
		if s != (Stats{}) {
			t.Errorf("%s: partial stats returned with the error: %+v", name, s)
		}
	}
	// Another object's place (AAD) fails from the first chunk.
	if _, err := ReadStats(bytes.NewReader(obj), key, ObjectAAD(uuid.New(), uuid.New(), "2026-05"), testCols); !errors.Is(err, ErrCorrupt) {
		t.Errorf("wrong aad: %v, want ErrCorrupt", err)
	}
}

func flip(b []byte, i int) []byte {
	c := append([]byte{}, b...)
	c[i] ^= 1
	return c
}

func TestReadStatsChecksHeader(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	aad := []byte("x")
	obj, _ := object(t, key, aad, csvRows(3))
	if _, err := ReadStats(bytes.NewReader(obj), key, aad, []string{"vehicle_id", "at", "lat", "shift_state", "other"}); !errors.Is(err, ErrMismatch) {
		t.Errorf("header mismatch: %v, want ErrMismatch", err)
	}
	if _, err := ReadStats(bytes.NewReader(obj), key, aad, []string{"a", "b"}); err == nil {
		t.Error("no at column accepted")
	}
}

func TestKeys(t *testing.T) {
	u := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	v := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	m := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	if got := ObjectKey("prod", u, v, m); got != "prod/v1/u=11111111-1111-4111-8111-111111111111/v=22222222-2222-4222-8222-222222222222/2026-05.csv.gz.enc" {
		t.Errorf("object key %s", got)
	}
	if got := ManifestKey("preview", u, v, m); !strings.HasPrefix(got, UserPrefix("preview", u)) || !strings.HasSuffix(got, "/2026-05.manifest.json") {
		t.Errorf("manifest key %s", got)
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := func(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }
	if _, on, err := ConfigFromEnv(env(nil)); on || err != nil {
		t.Errorf("unset: on=%v err=%v", on, err)
	}
	if _, _, err := ConfigFromEnv(env(map[string]string{"RIVOLT_ARCHIVE_ENV": "prod"})); err == nil {
		t.Error("env without keys accepted")
	}
	if _, _, err := ConfigFromEnv(env(map[string]string{"RIVOLT_ARCHIVE_ENV": "../prod", "RIVOLT_ARCHIVE_S3_ACCESS_KEY": "a", "RIVOLT_ARCHIVE_S3_SECRET_KEY": "b"})); err == nil {
		t.Error("path-like env accepted")
	}
	c, on, err := ConfigFromEnv(env(map[string]string{"RIVOLT_ARCHIVE_ENV": "preview", "RIVOLT_ARCHIVE_S3_ACCESS_KEY": "a", "RIVOLT_ARCHIVE_S3_SECRET_KEY": "b"}))
	if err != nil || !on || c.Bucket != "rivolt-telemetry-archive" || c.Region != "us-east-1" || c.Endpoint == "" {
		t.Errorf("defaults: %+v on=%v err=%v", c, on, err)
	}
}
