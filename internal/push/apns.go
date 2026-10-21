package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
)

// APNs delivery for the iPhone app (iOS design §3). What Apple is given is
// deliberately minimal: an id and a generic line. The notification's text is stored
// on our side (push_notifications) and fetched by the app's notification service
// extension over TLS, so Apple sees that a notification was sent, never what it says
// or which kind it is.

// APNsConfig is the server's Apple Push identity: the token-auth key (.p8) and the
// app's bundle id (the apns-topic). All four must be set for APNs to be on.
type APNsConfig struct {
	KeyID  string // RIVOLT_APNS_KEY_ID
	TeamID string // RIVOLT_APNS_TEAM_ID
	KeyPEM string // RIVOLT_APNS_KEY: the .p8 file's text
	Topic  string // RIVOLT_APNS_TOPIC: the bundle id
}

// Enabled reports whether every field is set.
func (c APNsConfig) Enabled() bool {
	return strings.TrimSpace(c.KeyID) != "" && strings.TrimSpace(c.TeamID) != "" &&
		strings.TrimSpace(c.KeyPEM) != "" && strings.TrimSpace(c.Topic) != ""
}

// Apple's two endpoints, one per device environment.
const (
	apnsProductionURL = "https://api.push.apple.com"
	apnsSandboxURL    = "https://api.sandbox.push.apple.com"
)

// Device environments.
const (
	EnvSandbox    = "sandbox"
	EnvProduction = "production"
)

// providerTokenTTL: Apple accepts a provider token for up to an hour and refuses
// one refreshed more often than every 20 minutes, so it is reused for 50.
const providerTokenTTL = 50 * time.Minute

// deviceTokenRE is an APNs device token as the app reports it: hex, 64 characters
// today, with room for longer ones.
var deviceTokenRE = regexp.MustCompile(`^[0-9a-fA-F]{64,200}$`)

// ValidDeviceToken reports whether t has the shape of an APNs device token.
func ValidDeviceToken(t string) bool { return deviceTokenRE.MatchString(t) }

// bundleIDRE is a reverse-DNS bundle identifier.
var bundleIDRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]{0,126}[A-Za-z0-9])?$`)

// APNsError is a refusal from Apple. Reason is Apple's code ("BadDeviceToken",
// "Unregistered", …), never anything from our payload.
type APNsError struct {
	Status int
	Reason string
}

func (e *APNsError) Error() string { return "apns: " + strconv.Itoa(e.Status) + " " + e.Reason }

// Dead reports that the device token will never work again: the row should go.
func (e *APNsError) Dead() bool {
	switch e.Reason {
	case "Unregistered", "BadDeviceToken", "DeviceTokenNotForTopic":
		return true
	}
	return e.Status == http.StatusGone
}

// APNs sends pushes to Apple. Safe for concurrent use.
type APNs struct {
	cfg  APNsConfig
	key  *ecdsa.PrivateKey
	hc   *http.Client
	urls map[string]string // environment -> base URL
	now  func() time.Time

	mu    sync.Mutex
	token string
	made  time.Time
}

// NewAPNs parses the key and returns a sender. The key is a PKCS#8 P-256 PEM (the
// .p8 file). It errors, naming nothing from the key, when the config is incomplete
// or the key is not usable.
func NewAPNs(cfg APNsConfig) (*APNs, error) {
	if !cfg.Enabled() {
		return nil, errors.New("apns: not configured")
	}
	if !bundleIDRE.MatchString(strings.TrimSpace(cfg.Topic)) {
		return nil, errors.New("apns: topic is not a bundle id")
	}
	block, _ := pem.Decode([]byte(cfg.KeyPEM))
	if block == nil {
		return nil, errors.New("apns: key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("apns: key is not a PKCS#8 private key")
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok || ec.Curve.Params().BitSize != 256 {
		return nil, errors.New("apns: key is not an ECDSA P-256 key")
	}
	cfg.Topic = strings.TrimSpace(cfg.Topic)
	return &APNs{
		cfg: cfg, key: ec, now: time.Now,
		hc:   &http.Client{Timeout: 15 * time.Second}, // net/http speaks HTTP/2 to Apple over TLS
		urls: map[string]string{EnvProduction: apnsProductionURL, EnvSandbox: apnsSandboxURL},
	}, nil
}

// Topic is the bundle id every push is addressed to (and devices must register with).
func (a *APNs) Topic() string { return a.cfg.Topic }

// providerToken returns the ES256 JWT Apple wants in the authorization header:
// iss = team id, iat = now, kid = key id; cached for providerTokenTTL.
func (a *APNs) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	if a.token != "" && now.Sub(a.made) < providerTokenTTL {
		return a.token, nil
	}
	sg, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: a.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", a.cfg.KeyID))
	if err != nil {
		return "", fmt.Errorf("apns: signer: %w", err)
	}
	claims, _ := json.Marshal(map[string]any{"iss": a.cfg.TeamID, "iat": now.Unix()})
	obj, err := sg.Sign(claims)
	if err != nil {
		return "", fmt.Errorf("apns: sign: %w", err)
	}
	tok, err := obj.CompactSerialize()
	if err != nil {
		return "", fmt.Errorf("apns: serialize: %w", err)
	}
	a.token, a.made = tok, now
	return tok, nil
}

func (a *APNs) dropToken() {
	a.mu.Lock()
	a.token = ""
	a.mu.Unlock()
}

// body is everything Apple is given besides the device token: a generic alert that
// the notification service extension replaces, mutable-content so it runs, and the id
// to fetch the real text with. No kind, no text, no URL.
func apnsBody(notificationID string) []byte {
	b, _ := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert":           map[string]string{"title": "Rivolt", "body": "Rivolt update"},
			"mutable-content": 1,
		},
		"id": notificationID,
	})
	return b
}

// Send delivers one notification (by id) to one device. An *APNsError comes back for
// Apple's refusals; Dead() says the token should be removed. A 403 ExpiredProviderToken
// refreshes the provider token and retries once.
func (a *APNs) Send(ctx context.Context, env, deviceToken, notificationID string) error {
	if !ValidDeviceToken(deviceToken) {
		return &APNsError{Status: http.StatusBadRequest, Reason: "BadDeviceToken"}
	}
	return a.deliver(ctx, env, deviceToken, apnsRequest{
		pushType: "alert", topic: a.cfg.Topic, priority: "10", body: apnsBody(notificationID), ttl: time.Hour,
	})
}

// apnsRequest is what differs between an alert and a Live Activity update.
type apnsRequest struct {
	pushType string
	topic    string
	priority string
	body     []byte
	ttl      time.Duration
}

// deliver posts one request to the token's path under the environment's host,
// retrying once when Apple says the provider token expired. The token must already
// be validated by the caller: it becomes part of the URL path.
func (a *APNs) deliver(ctx context.Context, env, token string, r apnsRequest) error {
	base, ok := a.urls[env]
	if !ok {
		return errors.New("apns: unknown environment")
	}
	for attempt := 0; ; attempt++ {
		err := a.sendOnce(ctx, base, token, r)
		var ae *APNsError
		if attempt == 0 && errors.As(err, &ae) && ae.Reason == "ExpiredProviderToken" {
			a.dropToken()
			continue
		}
		return err
	}
}

func (a *APNs) sendOnce(ctx context.Context, base, token string, ar apnsRequest) error {
	tok, err := a.providerToken()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/3/device/"+token, bytes.NewReader(ar.body))
	if err != nil {
		return errors.New("apns: request")
	}
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("apns-topic", ar.topic)
	req.Header.Set("apns-push-type", ar.pushType)
	req.Header.Set("apns-priority", ar.priority)
	req.Header.Set("apns-expiration", strconv.FormatInt(a.now().Add(ar.ttl).Unix(), 10))
	req.Header.Set("content-type", "application/json")
	resp, err := a.hc.Do(req)
	if err != nil {
		// The error text can carry the URL, which contains the device token.
		return errors.New("apns: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<10))
		return nil
	}
	var r struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&r)
	return &APNsError{Status: resp.StatusCode, Reason: r.Reason}
}
