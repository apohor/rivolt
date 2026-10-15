package logging

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
)

// processSalt makes IDHash values unlinkable across restarts and to any
// precomputed table of email hashes. Within one process the same input
// yields the same token, which is enough to correlate log lines.
var processSalt = func() []byte {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return b
}()

// IDHash returns a short salted hash standing in for an identifier
// (email, username) in log lines.
func IDHash(s string) string {
	h := sha256.New()
	h.Write(processSalt)
	h.Write([]byte(s))
	return hex.EncodeToString(h.Sum(nil))[:12]
}

var emailRe = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+(@|%40)[a-z0-9.\-]+`)

// ScrubEmails masks anything email-shaped (plain or URL-encoded) in
// third-party text before it's logged. Use for error strings from
// databases, IdPs and HTTP clients, which can echo the identifier.
func ScrubEmails(s string) string {
	if u, err := url.QueryUnescape(s); err == nil && emailRe.MatchString(u) {
		s = u
	}
	return emailRe.ReplaceAllString(s, "<email>")
}

// ScrubErr is ScrubEmails over err.Error(); empty for nil.
func ScrubErr(err error) string {
	if err == nil {
		return ""
	}
	return ScrubEmails(err.Error())
}
