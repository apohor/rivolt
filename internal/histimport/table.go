package histimport

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Caps on what a file may hold. The upload is capped at 50 MB by the caller; these bound what it can expand to.
const (
	MaxRows         = 500_000
	MaxCols         = 200
	MaxSessions     = 100_000 // drives or charges one file may produce; a decade of ten drives a day is under 40k
	MaxCSVBytes     = 200 << 20
	MaxXLSXZipBytes = 50 << 20
	MaxXLSXUnzipped = 200 << 20
)

// ErrTooBig: the file exceeds a cap (rows, columns, bytes).
var ErrTooBig = errors.New("histimport: file is too big")

// Table is a header plus a stream of rows. The row slice returned by Next is only valid until the next call.
type Table interface {
	Header() []string
	// Next returns the next non-empty row, or io.EOF.
	Next() ([]string, error)
}

type csvTable struct {
	r      *csv.Reader
	budget *csvBudget
	header []string
	rows   int
}

// OpenCSV reads a CSV with a header row. It skips a UTF-8 byte order mark and picks the delimiter (comma, semicolon or tab) from the header line.
func OpenCSV(src io.Reader) (Table, error) {
	guard := &csvBudget{r: &capReader{r: src, left: MaxCSVBytes}}
	br := bufio.NewReaderSize(guard, 64<<10)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	first, err := br.Peek(4096)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, err
	}
	line := first
	if i := bytes.IndexByte(first, '\n'); i >= 0 {
		line = first[:i]
	}
	r := csv.NewReader(br)
	r.Comma = sniffDelimiter(line)
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = true
	h, err := r.Read()
	if errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("histimport: the file is empty")
	}
	if err != nil {
		return nil, fmt.Errorf("histimport: the header line is not readable: %w", err)
	}
	if len(h) > MaxCols {
		return nil, ErrTooBig
	}
	guard.reset()
	t := &csvTable{r: r, budget: guard, header: append([]string(nil), h...)}
	for i := range t.header {
		t.header[i] = strings.TrimSpace(t.header[i])
	}
	return t, nil
}

func sniffDelimiter(line []byte) rune {
	best, n := ',', bytes.Count(line, []byte{','})
	for _, c := range []rune{';', '\t'} {
		if k := bytes.Count(line, []byte(string(c))); k > n {
			best, n = c, k
		}
	}
	return best
}

func (t *csvTable) Header() []string { return t.header }

func (t *csvTable) Next() ([]string, error) {
	for {
		rec, err := t.r.Read()
		if err == nil {
			t.budget.reset()
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				return nil, fmt.Errorf("histimport: a row is not valid CSV (line %d)", pe.Line)
			}
			return nil, err
		}
		if len(rec) > MaxCols {
			return nil, ErrTooBig
		}
		if blank(rec) {
			continue
		}
		if t.rows++; t.rows > MaxRows {
			return nil, ErrTooBig
		}
		return rec, nil
	}
}

func blank(rec []string) bool {
	for _, c := range rec {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// capReader fails with ErrTooBig when more than left bytes are read; a plain LimitReader would end the file quietly.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, ErrTooBig
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}
