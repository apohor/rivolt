package appleid

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
)

// Apple's server-to-server notifications (architect's design, DA-approved Oct 24). Apple POSTs a signed JWT to the
// URL registered on the Services ID when a user withdraws consent, deletes their Apple account, or turns the
// private-relay forwarding off or on.
//
// What the endpoint refuses or ignores, in order: a body over MaxNotificationBody; a payload not signed by Apple's
// keys for our Services ID; an `iat` more than an hour old; a payload already seen; a `sub` we don't hold. The last
// two are a silent 200: Apple should not retry them, and a caller learns nothing about who has an account.
//
// What it does: consent-revoked and account-delete sign the user out everywhere and forget the Apple token (the
// account and its data stay: no deletion on a third party's signal; the inactive-account purge is the only
// deletion). account-delete also emails the account's verified address that this sign-in method is gone.
// email-disabled and email-enabled set and clear users.email_undeliverable, but only for an account whose address is
// an Apple relay address: they are about that relay, so an account with its own address is left alone.
//
// Logged: the event type and the Rivolt user id. Never the `sub`, the relay address or any token.

// MaxNotificationBody caps the request body (a real notification is about 1 KB).
const MaxNotificationBody = 16 << 10

// NotificationFreshness is how old a notification's `iat` may be.
const NotificationFreshness = time.Hour

// notificationClockSkew is how far in the future an `iat` may be.
const notificationClockSkew = 5 * time.Minute

// Event types in the `events` claim.
const (
	EventConsentRevoked = "consent-revoked"
	EventAccountDelete  = "account-delete"
	EventEmailDisabled  = "email-disabled"
	EventEmailEnabled   = "email-enabled"
)

// Event is the decoded `events` claim of a notification.
type Event struct {
	Type string `json:"type"`
	Sub  string `json:"sub"`
}

// Claims are the fields of a verified notification the handler uses.
type Claims struct {
	IssuedAt time.Time
	Event    Event
}

// Verifier checks a notification JWT's signature, issuer and audience and returns its claims.
type Verifier interface {
	Verify(ctx context.Context, rawJWT string) (Claims, error)
}

// NewVerifier verifies against keys at issuer+"/auth/keys" (cached, refetched on an unknown key id) for the
// Services ID clientID. Apple's notifications carry an `exp` that we don't rely on: freshness is the `iat` rule.
func NewVerifier(ctx context.Context, issuer, clientID string) Verifier {
	return newVerifier(oidc.NewRemoteKeySet(ctx, strings.TrimRight(issuer, "/")+"/auth/keys"), issuer, clientID)
}

func newVerifier(ks oidc.KeySet, issuer, clientID string) Verifier {
	return &jwtVerifier{v: oidc.NewVerifier(issuer, ks, &oidc.Config{ClientID: clientID, SkipExpiryCheck: true})}
}

type jwtVerifier struct{ v *oidc.IDTokenVerifier }

func (j *jwtVerifier) Verify(ctx context.Context, raw string) (Claims, error) {
	tok, err := j.v.Verify(ctx, raw)
	if err != nil {
		return Claims{}, err
	}
	var c struct {
		IAT    int64           `json:"iat"`
		Events json.RawMessage `json:"events"`
	}
	if err := tok.Claims(&c); err != nil {
		return Claims{}, err
	}
	// Apple sends `events` as a JSON string holding an object; accept the object itself too.
	evJSON := []byte(c.Events)
	var s string
	if json.Unmarshal(c.Events, &s) == nil {
		evJSON = []byte(s)
	}
	var ev Event
	if err := json.Unmarshal(evJSON, &ev); err != nil {
		return Claims{}, errors.New("appleid: unreadable events claim")
	}
	return Claims{IssuedAt: time.Unix(c.IAT, 0), Event: ev}, nil
}

// SessionRevoker signs a user out everywhere (sessions.Store).
type SessionRevoker interface {
	RevokeAll(ctx context.Context, userID uuid.UUID) (int64, error)
}

// Mailer sends the account-delete notice (email.Client).
type Mailer interface {
	Send(ctx context.Context, m email.Message) error
}

// NotificationStore is the part of Store the endpoint uses.
type NotificationStore interface {
	UserBySub(ctx context.Context, sub string) (uuid.UUID, bool, error)
	DropToken(ctx context.Context, userID uuid.UUID) error
	MarkSeen(ctx context.Context, hash []byte) (bool, error)
	Unsee(ctx context.Context, hash []byte) error
	SetEmailUndeliverable(ctx context.Context, userID uuid.UUID, v bool) error
	DeliverableEmail(ctx context.Context, userID uuid.UUID) (string, error)
}

// Notifier serves the notifications endpoint. Wrap it with a body cap and a rate limit where it is mounted.
type Notifier struct {
	Verify   Verifier
	Store    NotificationStore
	Sessions SessionRevoker
	Mail     Mailer // nil: no email is sent
	Logger   *slog.Logger
	Now      func() time.Time
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now()
	}
	return time.Now()
}

// ServeHTTP handles POST /api/auth/apple/notifications.
func (n *Notifier) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	jwt, ok := readPayload(w, r)
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	claims, err := n.Verify.Verify(ctx, jwt)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	age := n.now().Sub(claims.IssuedAt)
	if age > NotificationFreshness || age < -notificationClockSkew {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	hash := sha256.Sum256([]byte(jwt))
	fresh, err := n.Store.MarkSeen(ctx, hash[:])
	if err != nil {
		http.Error(w, "try again", http.StatusInternalServerError)
		return
	}
	if !fresh {
		w.WriteHeader(http.StatusOK) // a replay: nothing to do
		return
	}
	if err := n.handle(ctx, claims.Event); err != nil {
		// Let Apple's retry through: forget we saw it.
		_ = n.Store.Unsee(ctx, hash[:])
		http.Error(w, "try again", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (n *Notifier) handle(ctx context.Context, ev Event) error {
	switch ev.Type {
	case EventConsentRevoked, EventAccountDelete, EventEmailDisabled, EventEmailEnabled:
	default:
		return nil // an event we don't act on
	}
	uid, found, err := n.Store.UserBySub(ctx, ev.Sub)
	if err != nil {
		return err
	}
	if !found {
		return nil // not ours: silent, no log
	}
	switch ev.Type {
	case EventConsentRevoked, EventAccountDelete:
		if _, err := n.Sessions.RevokeAll(ctx, uid); err != nil {
			return err
		}
		if err := n.Store.DropToken(ctx, uid); err != nil {
			return err
		}
		n.log().Info("apple notification", "event", ev.Type, "user_id", uid.String())
		if ev.Type == EventAccountDelete {
			n.sendAccountDeleteNotice(ctx, uid) // best effort: the sign-out is done
		}
	case EventEmailDisabled, EventEmailEnabled:
		if err := n.Store.SetEmailUndeliverable(ctx, uid, ev.Type == EventEmailDisabled); err != nil {
			return err
		}
		n.log().Info("apple notification", "event", ev.Type, "user_id", uid.String())
	}
	return nil
}

func (n *Notifier) log() *slog.Logger {
	if n.Logger != nil {
		return n.Logger
	}
	return slog.Default()
}

func (n *Notifier) sendAccountDeleteNotice(ctx context.Context, uid uuid.UUID) {
	if n.Mail == nil {
		return
	}
	to, err := n.Store.DeliverableEmail(ctx, uid)
	if err != nil || to == "" {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := n.Mail.Send(sctx, email.Message{
		To:             to,
		Subject:        "Sign in with Apple no longer works for your Rivolt account",
		IdempotencyKey: "apple-account-delete-" + uid.String(),
		Text: "You deleted your Apple Account, so Sign in with Apple can't be used for Rivolt any more, and we've\n" +
			"signed you out on your devices.\n\n" +
			"Your Rivolt account and its data are unchanged. To get back in, sign in another way you've set up.\n" +
			"If you have none, write to support@rivolt.dev from this address and we'll help. If you'd rather have\n" +
			"your account deleted, tell us and we'll do it.\n",
	}); err != nil {
		n.log().Warn("apple notification: email failed", "user_id", uid.String())
	}
}

// readPayload returns the JWT from the request: a form field "payload", or a JSON body {"payload": "..."}. The body
// is read through the caller's cap, and at most MaxNotificationBody here too.
func readPayload(w http.ResponseWriter, r *http.Request) (string, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxNotificationBody))
	if err != nil {
		return "", false
	}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var b struct {
			Payload string `json:"payload"`
		}
		if json.Unmarshal(body, &b) != nil || b.Payload == "" {
			return "", false
		}
		return b.Payload, true
	}
	form, err := parseForm(string(body))
	if err != nil || form == "" {
		return "", false
	}
	return form, true
}

// MarkSeen records a notification hash and reports whether it was new.
func (s *Store) MarkSeen(ctx context.Context, hash []byte) (bool, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO apple_notification_seen (event_hash) VALUES ($1) ON CONFLICT DO NOTHING`, hash)
	if err != nil {
		return false, fmt.Errorf("appleid: replay guard: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Unsee forgets a notification hash (the action failed; Apple will retry).
func (s *Store) Unsee(ctx context.Context, hash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM apple_notification_seen WHERE event_hash = $1`, hash)
	return err
}

// PurgeSeen deletes replay-guard rows older than two days (retention job).
func (s *Store) PurgeSeen(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM apple_notification_seen WHERE seen_at < now() - interval '2 days'`)
	if err != nil {
		return 0, fmt.Errorf("appleid: purge replay guard: %w", err)
	}
	return res.RowsAffected()
}

// relayDomain is Apple's private relay: the only address Apple's email-disabled / email-enabled events speak for.
const relayDomain = "@privaterelay.appleid.com"

// SetEmailUndeliverable marks the account's address as not reachable, for non-essential mail, or clears the mark.
// Marking only applies to an account whose address IS an Apple relay address: Apple's "forwarding off" event is about
// that relay, so an account with its own address (a linked account, or one that joined by sharing its email) is left
// alone. Clearing is always allowed.
func (s *Store) SetEmailUndeliverable(ctx context.Context, userID uuid.UUID, v bool) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET email_undeliverable = $2 WHERE id = $1 AND (NOT $2 OR lower(email) LIKE '%`+relayDomain+`')`, userID, v); err != nil {
		return fmt.Errorf("appleid: email flag: %w", err)
	}
	return nil
}

// DeliverableEmail is the account's verified address, or "" if it has none or it is marked undeliverable.
func (s *Store) DeliverableEmail(ctx context.Context, userID uuid.UUID) (string, error) {
	var e sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = $1 AND email_verified AND NOT email_undeliverable`, userID).Scan(&e)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return e.String, err
}

// parseForm returns the "payload" value of a urlencoded body.
func parseForm(body string) (string, error) {
	v, err := url.ParseQuery(body)
	if err != nil {
		return "", err
	}
	return v.Get("payload"), nil
}
