// Package archive holds the telemetry archive's building blocks (N19,
// telemetry-archive-design.md): per-user data keys and the encrypted stream
// every archive object is written in.
//
// Stream format (one object):
//
//	"RVA1" | 7-byte random nonce prefix | chunk_0 | chunk_1 | ... | chunk_n
//
// Each chunk is AES-256-GCM over up to ChunkSize plaintext bytes with nonce
// prefix | uint32 big-endian chunk index | 1 byte (1 on the final chunk, else
// 0), and the header plus the caller's associated data (the object's user,
// vehicle and month) as AAD. Every chunk but the last carries exactly
// ChunkSize bytes; the last carries 0..ChunkSize. The final-chunk flag in the
// nonce makes a truncated object fail to open instead of reading as shorter
// data, the index makes reordered chunks fail, and the AAD makes an object
// copied to another user's path fail. This is the STREAM construction (as in
// age and Tink's streaming AEAD).
package archive

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// ChunkSize is the plaintext per chunk.
const ChunkSize = 64 << 10

// KeySize is a data key's length (AES-256).
const KeySize = 32

const (
	magic       = "RVA1"
	prefixSize  = 7
	headerSize  = len(magic) + prefixSize
	tagSize     = 16
	sealedChunk = ChunkSize + tagSize
)

// ErrCorrupt is an object that fails authentication or is malformed.
var ErrCorrupt = errors.New("archive: object corrupt, truncated or not for this key")

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("archive: key is %d bytes, want %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func chunkNonce(prefix []byte, index uint32, last bool) []byte {
	n := make([]byte, 12)
	copy(n, prefix)
	binary.BigEndian.PutUint32(n[prefixSize:], index)
	if last {
		n[11] = 1
	}
	return n
}

type encryptWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	aad    []byte
	buf    []byte
	index  uint32
	closed bool
	err    error
}

// NewEncryptWriter returns a writer that encrypts everything written to it
// into w. Close must be called to write the final chunk; it does not close w.
// aad binds the object to its place (user, vehicle, month).
func NewEncryptWriter(w io.Writer, key, aad []byte) (io.WriteCloser, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	header := make([]byte, headerSize)
	copy(header, magic)
	if _, err := rand.Read(header[len(magic):]); err != nil {
		return nil, err
	}
	if _, err := w.Write(header); err != nil {
		return nil, err
	}
	return &encryptWriter{w: w, aead: aead, header: header, aad: append(append([]byte{}, header...), aad...),
		buf: make([]byte, 0, ChunkSize)}, nil
}

func (e *encryptWriter) Write(p []byte) (int, error) {
	if e.err != nil {
		return 0, e.err
	}
	if e.closed {
		return 0, errors.New("archive: write after close")
	}
	n := 0
	for len(p) > 0 {
		// A full buffer is sealed only once more data arrives, so the last
		// chunk is always written by Close with the final flag.
		if len(e.buf) == ChunkSize {
			if err := e.seal(false); err != nil {
				return n, err
			}
		}
		k := copy(e.buf[len(e.buf):ChunkSize], p)
		e.buf = e.buf[:len(e.buf)+k]
		p = p[k:]
		n += k
	}
	return n, nil
}

func (e *encryptWriter) seal(last bool) error {
	if e.index == math.MaxUint32 {
		e.err = errors.New("archive: object too large")
		return e.err
	}
	out := e.aead.Seal(nil, chunkNonce(e.header[len(magic):], e.index, last), e.buf, e.aad)
	if _, err := e.w.Write(out); err != nil {
		e.err = err
		return err
	}
	e.index++
	e.buf = e.buf[:0]
	return nil
}

func (e *encryptWriter) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	if e.err != nil {
		return e.err
	}
	return e.seal(true)
}

type decryptReader struct {
	r     io.Reader
	aead  cipher.AEAD
	nonce []byte // the header's nonce prefix
	aad   []byte
	in    []byte // one sealed chunk plus one byte of look-ahead
	ahead int    // bytes of the next chunk already read into in
	out   []byte // decrypted bytes not yet returned
	index uint32
	done  bool
	err   error
}

// NewDecryptReader returns a reader of the plaintext of an object written by
// NewEncryptWriter with the same key and aad. Any tampering, truncation,
// reordering or wrong key/aad is an ErrCorrupt read error; the plaintext read
// before it must be discarded by the caller.
func NewDecryptReader(r io.Reader, key, aad []byte) (io.Reader, error) {
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(r, header); err != nil || string(header[:len(magic)]) != magic {
		return nil, ErrCorrupt
	}
	return &decryptReader{r: r, aead: aead, nonce: header[len(magic):], aad: append(append([]byte{}, header...), aad...),
		in: make([]byte, sealedChunk+1)}, nil
}

func (d *decryptReader) Read(p []byte) (int, error) {
	for len(d.out) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		if d.done {
			return 0, io.EOF
		}
		d.err = d.next()
	}
	n := copy(p, d.out)
	d.out = d.out[n:]
	return n, nil
}

// next opens one chunk. It reads one byte past a full chunk to learn whether
// more follow, which decides the final flag the chunk must carry.
func (d *decryptReader) next() error {
	n, err := io.ReadFull(d.r, d.in[d.ahead:])
	n += d.ahead
	d.ahead = 0
	last := false
	switch {
	case err == nil: // a full chunk and at least one more byte: not the last
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		last = true
	default:
		return err
	}
	size := n
	if !last {
		size = sealedChunk
	}
	if size < tagSize {
		return ErrCorrupt
	}
	plain, oerr := d.aead.Open(d.in[:0:size], chunkNonce(d.nonce, d.index, last), d.in[:size], d.aad)
	if oerr != nil {
		return ErrCorrupt
	}
	if !last {
		// Keep the look-ahead byte for the next chunk, after copying out the
		// plaintext (Open decrypted in place over d.in).
		out := append([]byte(nil), plain...)
		d.in[0] = d.in[sealedChunk]
		d.ahead = 1
		d.out = out
		d.index++
		return nil
	}
	d.out = append([]byte(nil), plain...)
	d.done = true
	return nil
}
