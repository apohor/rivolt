package archive

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"
)

// An archive object is one (user, vehicle, month) of vehicle_state: a CSV
// whose header is the table's column list and whose rows are ordered by at,
// gzipped, then encrypted with the user's data key (stream.go). Next to it a
// manifest records what verification checked; it holds counts, times and
// hashes, never telemetry values.

// Schema names the object format; the manifest carries it.
const Schema = "rivolt-archive/v1"

// TimeLayout is how timestamps appear in the CSV and the manifest (UTC,
// microseconds, as Postgres stores them).
const TimeLayout = "2006-01-02T15:04:05.000000Z"

var envName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// MonthString is the month's name in object keys and in ObjectAAD: "2026-05".
func MonthString(m time.Time) string { return m.UTC().Format("2006-01") }

// UserPrefix is everything archived for uid in env; account deletion lists
// and deletes under it.
func UserPrefix(env string, uid uuid.UUID) string {
	return env + "/v1/u=" + uid.String() + "/"
}

// ObjectKey is the object for one user, vehicle and month.
func ObjectKey(env string, uid, vehicleID uuid.UUID, month time.Time) string {
	return UserPrefix(env, uid) + "v=" + vehicleID.String() + "/" + MonthString(month) + ".csv.gz.enc"
}

// ManifestKey is the object's manifest.
func ManifestKey(env string, uid, vehicleID uuid.UUID, month time.Time) string {
	return UserPrefix(env, uid) + "v=" + vehicleID.String() + "/" + MonthString(month) + ".manifest.json"
}

// Manifest describes one object.
type Manifest struct {
	Schema       string    `json:"schema"`
	Columns      []string  `json:"columns"`
	Rows int64 `json:"rows"`
	// FirstAt and LastAt are checked at verify and recorded in archive_months,
	// but not written to the plaintext manifest: after a crypto-shred the
	// manifest lingers until the deletion job runs, and should say nothing
	// about when the user drove (DA).
	FirstAt      string    `json:"-"`
	LastAt       string    `json:"-"`
	SHA256Plain  string    `json:"sha256_plain"`
	SHA256Object string    `json:"sha256_object"`
	Bytes        int64     `json:"bytes"`
	CreatedAt    time.Time `json:"created_at"`
}

// objectWriter takes the plain CSV and writes the object: it hashes the
// plaintext, gzips and encrypts.
type objectWriter struct {
	plain hash.Hash
	gz    *gzip.Writer
	enc   io.WriteCloser
}

func newObjectWriter(dst io.Writer, key, aad []byte) (*objectWriter, error) {
	enc, err := NewEncryptWriter(dst, key, aad)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewWriterLevel(enc, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	return &objectWriter{plain: sha256.New(), gz: gz, enc: enc}, nil
}

func (w *objectWriter) Write(p []byte) (int, error) {
	w.plain.Write(p)
	return w.gz.Write(p)
}

// Close flushes gzip and writes the final encrypted chunk.
func (w *objectWriter) Close() error {
	if err := w.gz.Close(); err != nil {
		return err
	}
	return w.enc.Close()
}

func (w *objectWriter) plainSHA256() string { return hex.EncodeToString(w.plain.Sum(nil)) }

// Stats is what reading an object back finds.
type Stats struct {
	Rows        int64
	FirstAt     string
	LastAt      string
	SHA256Plain string
}

// ErrMismatch is an object that decrypts but doesn't hold what was written.
var ErrMismatch = errors.New("archive: object doesn't match what was written")

// ReadStats reads a whole object, decrypting and decompressing it, and counts
// its CSV rows. The header must be columns, and at must be one of them. It
// reads to the end of the encrypted stream: a corrupt or truncated object is
// an error even after rows were read (the STREAM construction authenticates
// the end last), and no Stats are returned with an error.
func ReadStats(r io.Reader, key, aad []byte, columns []string) (Stats, error) {
	atCol := slices.Index(columns, "at")
	if atCol < 0 {
		return Stats{}, fmt.Errorf("archive: column list has no at")
	}
	dec, err := NewDecryptReader(r, key, aad)
	if err != nil {
		return Stats{}, err
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return Stats{}, fmt.Errorf("archive: gzip: %w", err)
	}
	plain := sha256.New()
	cr := csv.NewReader(io.TeeReader(gz, plain))
	cr.FieldsPerRecord = len(columns)
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return Stats{}, fmt.Errorf("archive: header: %w", err)
	}
	if !slices.Equal(header, columns) {
		return Stats{}, fmt.Errorf("%w: header %v, want %v", ErrMismatch, header, columns)
	}
	var s Stats
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Stats{}, fmt.Errorf("archive: row %d: %w", s.Rows+1, err)
		}
		if s.Rows == 0 {
			s.FirstAt = rec[atCol]
		}
		s.LastAt = rec[atCol]
		s.Rows++
	}
	// gzip has verified its checksum at its end; drain whatever follows so the
	// decrypting reader authenticates the final chunk.
	if _, err := io.Copy(io.Discard, dec); err != nil {
		return Stats{}, err
	}
	s.SHA256Plain = hex.EncodeToString(plain.Sum(nil))
	return s, nil
}
