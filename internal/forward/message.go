package forward

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is the message format version. A message with another version is dropped and counted rejected.
const Version = 1

// Size caps (bytes). A request is one Redis list element; a reply is one element of a short-lived list.
const (
	MaxRequestBytes = 64 << 10
	MaxReplyBytes   = 256 << 10
)

// KeyLen is the length of the HMAC key (the install secret), in bytes.
const KeyLen = 32

// EnvKey is the environment variable that carries the key, base64 (32 random bytes: 44 characters). It is optional until the
// forwarder flag is on. Vault: kv/rivolt/forward and kv/rivolt-preview/forward, property hmac_key.
const EnvKey = "RIVOLT_FORWARD_HMAC_KEY"

// Request is what a requester pushes to the owner's list. Sig covers every other field.
type Request struct {
	V         int             `json:"v"`
	ID        string          `json:"id"`
	User      string          `json:"user"`
	Group     string          `json:"group"`
	Epoch     int64           `json:"epoch"`
	Kind      Kind            `json:"kind"`
	Args      json.RawMessage `json:"args"`
	ExpiresMS int64           `json:"expires_ms"`
	Sig       string          `json:"sig"`
}

// Reply is what the owner pushes back to the requester's one-shot list. Body is kind-specific JSON; Sig covers every other field.
type Reply struct {
	V      int             `json:"v"`
	ID     string          `json:"id"`
	User   string          `json:"user"`
	OK     bool            `json:"ok"`
	Status int             `json:"status"`
	Class  string          `json:"class,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
	Sig    string          `json:"sig"`
}

var (
	// ErrBadKey: the key is missing, not base64, or not KeyLen bytes.
	ErrBadKey = errors.New("forward: the HMAC key must be 32 random bytes, base64-encoded")
	// ErrBadSig: the signature is missing, malformed or wrong.
	ErrBadSig = errors.New("forward: bad signature")
	// ErrTooBig: a message over its size cap.
	ErrTooBig = errors.New("forward: message too big")
	// ErrBadMessage: not a message of this version, or a malformed one.
	ErrBadMessage = errors.New("forward: bad message")
)

// ParseKey decodes the base64 key and checks its length. The value is never logged or returned in an error.
func ParseKey(b64 string) ([]byte, error) {
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil || len(k) != KeyLen {
		return nil, ErrBadKey
	}
	return k, nil
}

// Signer signs and verifies messages with two subkeys derived from the install secret, one per direction (domain separation: a
// reply cannot be replayed as a request, nor the reverse). The install secret itself is not kept.
type Signer struct {
	req, rep []byte
}

// NewSigner derives the subkeys from key (KeyLen bytes).
func NewSigner(key []byte) (*Signer, error) {
	if len(key) != KeyLen {
		return nil, ErrBadKey
	}
	sub := func(label string) []byte {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(label))
		return m.Sum(nil)
	}
	return &Signer{req: sub("rivolt/forward/request/v1"), rep: sub("rivolt/forward/reply/v1")}, nil
}

// digest is the lower-case hex sha256 of the compacted JSON (a raw value is signed by its digest, so the signed string stays short
// and a re-spaced copy of the same JSON verifies).
func digest(raw json.RawMessage) string {
	var buf bytes.Buffer
	if len(raw) == 0 || json.Compact(&buf, raw) != nil {
		buf.Reset()
		buf.Write(raw)
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

func mac(key []byte, fields ...string) string {
	m := hmac.New(sha256.New, key)
	for _, f := range fields {
		// Length-prefixed so no field can bleed into the next.
		m.Write([]byte(strconv.Itoa(len(f))))
		m.Write([]byte{':'})
		m.Write([]byte(f))
	}
	return hex.EncodeToString(m.Sum(nil))
}

func (r *Request) fields() []string {
	return []string{strconv.Itoa(r.V), r.ID, r.User, r.Group, strconv.FormatInt(r.Epoch, 10), string(r.Kind), digest(r.Args), strconv.FormatInt(r.ExpiresMS, 10)}
}

func (r *Reply) fields() []string {
	return []string{strconv.Itoa(r.V), r.ID, r.User, strconv.FormatBool(r.OK), strconv.Itoa(r.Status), r.Class, digest(r.Body)}
}

// SignRequest sets r.Sig.
func (s *Signer) SignRequest(r *Request) { r.Sig = mac(s.req, r.fields()...) }

// VerifyRequest checks r.Sig in constant time. It looks at nothing else about the message.
func (s *Signer) VerifyRequest(r *Request) error {
	return verify(r.Sig, mac(s.req, r.fields()...))
}

// SignReply sets r.Sig.
func (s *Signer) SignReply(r *Reply) { r.Sig = mac(s.rep, r.fields()...) }

// VerifyReply checks r.Sig in constant time.
func (s *Signer) VerifyReply(r *Reply) error {
	return verify(r.Sig, mac(s.rep, r.fields()...))
}

func verify(got, want string) error {
	g, err := hex.DecodeString(got)
	if err != nil || len(g) != sha256.Size {
		return ErrBadSig
	}
	w, _ := hex.DecodeString(want)
	if !hmac.Equal(g, w) {
		return ErrBadSig
	}
	return nil
}

// DecodeRequest parses one list element: size cap, strict JSON (unknown fields rejected), version, id shape. It does not check
// the signature or the args; the owner verifies the signature first, then calls Spec.Validate.
func DecodeRequest(b []byte) (*Request, error) {
	if len(b) > MaxRequestBytes {
		return nil, ErrTooBig
	}
	var r Request
	if err := strictDecode(b, &r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadMessage, err)
	}
	if r.V != Version || !ValidID(r.ID) {
		return nil, ErrBadMessage
	}
	return &r, nil
}

// DecodeReply parses one reply element the same way.
func DecodeReply(b []byte) (*Reply, error) {
	if len(b) > MaxReplyBytes {
		return nil, ErrTooBig
	}
	var r Reply
	if err := strictDecode(b, &r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadMessage, err)
	}
	if r.V != Version || !ValidID(r.ID) {
		return nil, ErrBadMessage
	}
	return &r, nil
}

func strictDecode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() { // trailing data after the object
		return errors.New("trailing data")
	}
	return nil
}

var idRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

// ValidID reports whether s is a request id: 32 lower-case hex characters (128 bits). The owner builds the reply key from an id it
// has validated, so a request cannot name an arbitrary key.
func ValidID(s string) bool { return idRE.MatchString(s) }

// NewID returns a fresh random request id: 128 bits from crypto/rand, never derived from a user, vehicle or time.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
