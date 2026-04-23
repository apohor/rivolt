package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

// Payload is the JSON body we deliver to the service worker. The SW
// parses it in its 'push' handler and turns it into a Notification.
type Payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// Tag collapses multiple notifications in the same category into a
	// single entry on the device (so a run of back-to-back shots doesn't
	// spam the notification center).
	Tag string `json:"tag,omitempty"`
	// URL is the path the PWA should navigate to when the user taps the
	// notification. Must be same-origin; the SW uses clients.openWindow.
	URL string `json:"url,omitempty"`
	// Kind lets the SW / UI disambiguate without string-matching titles.
	Kind string `json:"kind,omitempty"`
}

// Sender is the subset of webpush used by Service. Extracted so tests
// can feed a fake without spinning up a push service.
type Sender interface {
	SendNotification(message []byte, sub *webpush.Subscription, options *webpush.Options) (*http.Response, error)
}

// defaultSender wraps webpush.SendNotificationWithContext into the
// Sender signature.
type defaultSender struct{}

func (defaultSender) SendNotification(msg []byte, sub *webpush.Subscription, opts *webpush.Options) (*http.Response, error) {
	return webpush.SendNotification(msg, sub, opts)
}

// Service sends push notifications to every stored subscription that
// opted in for the given event. All public methods are non-blocking:
// they hand the work off to a background goroutine so they can be called
// from latency-sensitive spots (recorder flush, analysis completion)
// without holding anything up.
type Service struct {
	store  *Store
	vapid  VAPID
	logger *slog.Logger
	sender Sender

	// ttlSeconds is the push server's "keep-alive" budget for a
	// notification. 24h is the common default — long enough to survive a
	// user whose phone was offline overnight, short enough not to deliver
	// "shot finished" 4 days later.
	ttlSeconds int
}

// NewService builds a Service. Callers should only construct one per
// process; it's safe for concurrent use.
func NewService(store *Store, vapid VAPID, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:      store,
		vapid:      vapid,
		logger:     logger,
		sender:     defaultSender{},
		ttlSeconds: int((24 * time.Hour).Seconds()),
	}
}

// PublicKey exposes the VAPID public key so the API layer can return it
// to the frontend for pushManager.subscribe().
func (s *Service) PublicKey() string {
	if s == nil {
		return ""
	}
	return s.vapid.PublicKey
}

// NotifyChargingDone fires when a charge session reaches the target SoC.
// Runs in its own goroutine — callers need not block.
func (s *Service) NotifyChargingDone(sessionID, summary string) {
	if s == nil {
		return
	}
	title := "Charging complete"
	body := "Tap to review the session."
	if summary != "" {
		body = summary
	}
	go s.broadcast(Payload{
		Title: title,
		Body:  body,
		Tag:   "rivolt-charging-done",
		URL:   "/charges/" + sessionID,
		Kind:  "charging_done",
	}, selectChargingDone)
}

// NotifyPlugInReminder fires when the vehicle is parked at home below a
// configured SoC threshold and hasn't been plugged in by the user's
// cutoff time.
func (s *Service) NotifyPlugInReminder(soc int) {
	if s == nil {
		return
	}
	go s.broadcast(Payload{
		Title: "Plug in reminder",
		Body:  fmt.Sprintf("Battery is at %d%%. Plug in to be ready for tomorrow.", soc),
		Tag:   "rivolt-plug-in",
		URL:   "/",
		Kind:  "plug_in_reminder",
	}, selectPlugInReminder)
}

// NotifyAnomaly fires when the AI coach has flagged something unusual
// (sudden range drop, phantom-drain spike, unexpected BMS behavior).
func (s *Service) NotifyAnomaly(title, body, deepLink string) {
	if s == nil {
		return
	}
	go s.broadcast(Payload{
		Title: title,
		Body:  body,
		Tag:   "rivolt-anomaly",
		URL:   deepLink,
		Kind:  "anomaly",
	}, selectAnomaly)
}

// SendTest delivers a single test notification to one endpoint. Used by
// the "send test" button in Settings so operators can verify the pipe
// without having to actually pull a shot.
func (s *Service) SendTest(ctx context.Context, endpoint string) error {
	subs, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if sub.Endpoint == endpoint {
			return s.sendOne(ctx, sub, Payload{
				Title: "Rivolt test notification",
				Body:  "Push is working. Drive your truck to see the real thing.",
				Tag:   "rivolt-test",
				URL:   "/",
				Kind:  "test",
			})
		}
	}
	return errors.New("subscription not found")
}

type selector func(Subscription) bool

func selectChargingDone(s Subscription) bool   { return s.OnChargingDone }
func selectPlugInReminder(s Subscription) bool { return s.OnPlugInReminder }
func selectAnomaly(s Subscription) bool        { return s.OnAnomaly }

// broadcast fans a payload out to every stored subscription matching
// the selector. Each send is independent: one bad subscription can't
// block the others, and 404/410 responses purge the dead subscription.
func (s *Service) broadcast(p Payload, sel selector) {
	// Deliberately use a fresh context independent of any request-scoped
	// one: shot-finished is usually invoked from a goroutine that will
	// outlive the triggering HTTP request anyway.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	subs, err := s.store.List(ctx)
	if err != nil {
		s.logger.Warn("push: list subs failed", "err", err.Error())
		return
	}
	if len(subs) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, sub := range subs {
		if !sel(sub) {
			continue
		}
		wg.Add(1)
		go func(sub Subscription) {
			defer wg.Done()
			if err := s.sendOne(ctx, sub, p); err != nil {
				s.logger.Warn("push: send failed",
					"host", endpointHost(sub.Endpoint),
					"kind", p.Kind,
					"err", err.Error())
			}
		}(sub)
	}
	wg.Wait()
}

// sendOne delivers the payload to a single subscription. If the push
// service reports the subscription is gone (404/410) we purge it so the
// DB doesn't accumulate dead rows.
func (s *Service) sendOne(ctx context.Context, sub Subscription, p Payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}

	wsub := &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys: webpush.Keys{
			P256dh: sub.P256dh,
			Auth:   sub.Auth,
		},
	}
	// Apple's web.push.apple.com will batch or silently drop
	// normal-urgency pushes on a backgrounded device; high is what you
	// want for user-visible notifications that need to arrive promptly.
	// FCM/Mozilla treat the two similarly in practice.
	res, err := s.sender.SendNotification(body, wsub, &webpush.Options{
		Subscriber:      s.vapid.Subject,
		VAPIDPublicKey:  s.vapid.PublicKey,
		VAPIDPrivateKey: s.vapid.PrivateKey,
		TTL:             s.ttlSeconds,
		Urgency:         webpush.UrgencyHigh,
	})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	// Read (up to 1 KiB of) the response body so Apple's / Mozilla's /
	// FCM's error messages make it into our logs. Apple in particular
	// returns human-readable JSON like {"reason":"BadJwtToken"} that is
	// invaluable for debugging iOS-only failures.
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1024))
	// Drain anything we didn't read so the underlying connection is
	// safe to reuse.
	_, _ = io.Copy(io.Discard, res.Body)

	host := endpointHost(sub.Endpoint)
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		// Browser has told the push service this subscription is dead
		// (uninstalled PWA, revoked permission, etc.). Clean it up.
		if delErr := s.store.Delete(ctx, sub.Endpoint); delErr != nil {
			s.logger.Warn("push: purge dead sub failed", "err", delErr.Error())
		} else {
			s.logger.Info("push: purged dead subscription",
				"host", host,
				"status", res.StatusCode,
				"body", previewBody(respBody))
		}
		return nil
	case res.StatusCode >= 200 && res.StatusCode < 300:
		s.logger.Debug("push: delivered",
			"host", host,
			"status", res.StatusCode,
			"kind", p.Kind)
		return nil
	default:
		// 400 (bad JWT / bad encryption), 401/403 (VAPID rejected),
		// 413 (payload too big), 429 (rate limited), 5xx (provider
		// down). Apple returns descriptive bodies; log them.
		s.logger.Warn("push: non-2xx response",
			"host", host,
			"status", res.StatusCode,
			"body", previewBody(respBody),
			"kind", p.Kind)
		return &sendError{status: res.StatusCode, body: previewBody(respBody)}
	}
}

// endpointHost extracts the hostname from a push endpoint URL for
// logging. Returns the raw string on parse failure; we never want the
// logging path to error out.
func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return truncate(endpoint, 60)
	}
	return u.Host
}

// previewBody renders a short, single-line version of a push service's
// error body so slog's key=value output stays readable. Apple replies
// are already short JSON; FCM / Mozilla are sometimes plain text.
func previewBody(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

type sendError struct {
	status int
	body   string
}

func (e *sendError) Error() string {
	if e.body != "" {
		return "push " + itoa(e.status) + ": " + e.body
	}
	return "push status " + itoa(e.status)
}

// itoa avoids pulling strconv for one line; status codes are small.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [10]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// truncate shortens a string for logging without pulling fmt. Push
// endpoints are 300+ chars each; the last 60 is enough to tell which
// provider is failing (fcm/mozilla/windows).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
