package histimport

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"io"
)

// Per-token and per-record limits. The byte caps in table.go bound a whole file; these bound what one token, record or directory may make the
// decoders hold, because encoding/xml and encoding/csv build a whole token or record before returning it.
const (
	maxXMLRun       = 1 << 20  // bytes between two '<' (one text node or one tag with its attributes)
	maxXMLDepth     = 64       // open elements; encoding/xml keeps one stack entry per open element
	maxXMLCell      = 64 << 10 // characters one cell may hold (Excel's own limit is 32,767)
	maxSharedBytes  = 16 << 20 // all shared strings together, with a 16-byte header each
	maxCSVRecord    = 256 << 10
	maxLabel        = 128     // bytes kept of a vehicle name or currency
	maxZipDirectory = 1 << 20 // bytes of central directory; 2000 entries with ordinary names are far below it
	maxZipEntries   = 2000
)

// xmlRunGuard fails with ErrTooBig when more than maxXMLRun bytes pass without a '<': a 200 MB text node or one huge tag would otherwise be
// buffered whole by the decoder.
type xmlRunGuard struct {
	r   io.Reader
	run int
}

func (g *xmlRunGuard) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	for _, b := range p[:n] {
		if b == '<' {
			g.run = 0
		} else if g.run++; g.run > maxXMLRun {
			return 0, ErrTooBig
		}
	}
	return n, err
}

// xmlDepth counts open elements and fails past maxXMLDepth.
type xmlDepth int

func (d *xmlDepth) see(tok xml.Token) error {
	switch tok.(type) {
	case xml.StartElement:
		if *d++; *d > maxXMLDepth {
			return ErrTooBig
		}
	case xml.EndElement:
		*d--
	}
	return nil
}

// csvGuard fails with ErrTooBig when one record runs past maxCSVRecord bytes or holds more than MaxCols delimiters outside quotes, before
// encoding/csv has built a field index for it. It tracks quote parity the way a quoted newline needs; a stray quote can only make it stricter.
type csvGuard struct {
	r      io.Reader
	delim  byte // zero until the header has been sniffed
	quoted bool
	bytes  int
	delims int
}

func (g *csvGuard) Read(p []byte) (int, error) {
	n, err := g.r.Read(p)
	for _, b := range p[:n] {
		g.bytes++
		switch {
		case b == '"':
			g.quoted = !g.quoted
		case b == '\n' && !g.quoted:
			g.bytes, g.delims = 0, 0
		case b == g.delim && g.delim != 0 && !g.quoted:
			g.delims++
		}
		if g.bytes > maxCSVRecord || g.delims > MaxCols {
			return 0, ErrTooBig
		}
	}
	return n, err
}

// checkZipDirectory reads the end-of-central-directory record before zip.NewReader does, because NewReader builds a File for every entry it
// finds from the directory offset up to that record. It refuses zip64, more than maxZipEntries entries, and a directory over maxZipDirectory bytes.
func checkZipDirectory(src io.ReaderAt, size int64) error {
	n := int64(22 + 65535)
	if n > size {
		n = size
	}
	buf := make([]byte, n)
	if _, err := src.ReadAt(buf, size-n); err != nil && err != io.EOF {
		return errNotXLSX
	}
	i := bytes.LastIndex(buf, []byte("PK\x05\x06"))
	if i < 0 || len(buf)-i < 22 {
		return errNotXLSX
	}
	count := binary.LittleEndian.Uint16(buf[i+10:])
	dirSize := binary.LittleEndian.Uint32(buf[i+12:])
	dirOff := int64(binary.LittleEndian.Uint32(buf[i+16:]))
	eocd := size - n + int64(i)
	if count == 0xFFFF || dirSize == 0xFFFFFFFF || dirOff == 0xFFFFFFFF {
		return ErrTooBig
	}
	if int(count) > maxZipEntries || int64(dirSize) > maxZipDirectory || dirOff > eocd || eocd-dirOff > maxZipDirectory {
		return ErrTooBig
	}
	return nil
}
