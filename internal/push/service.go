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
	// apns, when set, also delivers to the user's iPhone app installs (nil: off).
	apns *APNs
	live bool // Live Activity updates (RIVOLT_APNS_LIVE); needs apns

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

// NotifyChargingDone fires when a charge session reaches its terminal
// state. The provided store must be scoped to the owning user.
func (s *Service) NotifyChargingDone(store *Store, sessionID, summary string) {
	if s == nil || store == nil {
		return
	}
	title := "Charging complete"
	body := "Tap to review the session."
	if summary != "" {
		body = summary
	}
	go s.broadcast(store, Payload{
		Title: title,
		Body:  body,
		Tag:   "rivolt-charging-done",
		URL:   "/charges/" + sessionID,
		Kind:  "charging_done",
	}, selectChargingDone)
}

// PlugInReminderPayload is the plug-in reminder: functional, SoC and the
// user's own threshold, no place and no promotion.
func PlugInReminderPayload(soc, threshold int) Payload {
	return Payload{
		Title: "Plug in tonight?",
		Body:  fmt.Sprintf("Battery at %d%%. Below your %d%% reminder level.", soc, threshold),
		Tag:   "rivolt-plug-in",
		URL:   "/",
		Kind:  "plug_in_reminder",
	}
}

// DeliverPlugInReminder sends the plug-in reminder to every subscription of
// the store's user that has it on, and waits. It reports how many sends
// succeeded; an error means none did, or the subscriptions could not be read.
// Zero subscriptions with the reminder on is an error too: nothing was
// delivered.
func (s *Service) DeliverPlugInReminder(ctx context.Context, store *Store, soc, threshold int) (int, error) {
	if s == nil || store == nil {
		return 0, errors.New("push: not configured")
	}
	subs, err := store.List(ctx)
	if err != nil {
		return 0, err
	}
	p := PlugInReminderPayload(soc, threshold)
	var ok int
	var last error
	for _, sub := range subs {
		if !selectPlugInReminder(sub) {
			continue
		}
		if err := s.sendOne(ctx, store, sub, p); err != nil {
			last = err
			continue
		}
		ok++
	}
	ok += s.fanAPNs(ctx, store, p)
	if ok == 0 {
		if last == nil {
			last = errors.New("push: no subscription has the plug-in reminder on")
		}
		return 0, last
	}
	return ok, nil
}

// LeftOpenPayload is the left-open alert. The text is fixed apart from the vehicle's model (never the user's own
// nickname for it: it shows on the lock screen) and which closures are open (a door, the frunk, the liftgate, the
// tailgate, the tonneau). It names no place and says nothing about the truck being safe or unsafe: a missing alert
// is never a promise that nothing is open. The tap opens the app.
func LeftOpenPayload(model string, kinds []string, minutes int) Payload {
	what := "something"
	if len(kinds) > 0 {
		what = JoinClosures(kinds)
	}
	verb := "has" // "has" for one closure (or "something"), "have" for several
	if n := countClosures(kinds); n > 1 {
		verb = "have"
	}
	truck := "your vehicle"
	if m := strings.TrimSpace(model); m != "" {
		truck = "your " + m
	}
	return Payload{
		Title: "Left open",
		Body:  fmt.Sprintf("%s on %s %s been open for %d minutes.", capitalize(what), truck, verb, minutes),
		Tag:   "rivolt-left-open",
		URL:   "/",
		Kind:  "left_open",
	}
}

// closureWords are the closures the alert names, in the order they are listed.
var closureWords = []struct{ kind, words string }{
	{"frunk", "the frunk"}, {"door", "a door"}, {"liftgate", "the liftgate"}, {"tailgate", "the tailgate"}, {"tonneau", "the tonneau"},
}

// JoinClosures names the open closures: "the frunk", "the frunk and a door" (in the order of closureWords).
// Unknown kinds are ignored; with none left it is "something".
func JoinClosures(kinds []string) string {
	have := map[string]bool{}
	for _, k := range kinds {
		have[k] = true
	}
	var parts []string
	for _, c := range closureWords {
		if have[c.kind] {
			parts = append(parts, c.words)
		}
	}
	switch len(parts) {
	case 0:
		return "something"
	case 1:
		return parts[0]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

// countClosures counts the distinct closure kinds JoinClosures would name.
func countClosures(kinds []string) int {
	have := map[string]bool{}
	for _, k := range kinds {
		have[k] = true
	}
	n := 0
	for _, c := range closureWords {
		if have[c.kind] {
			n++
		}
	}
	return n
}

// DeliverLeftOpen sends the left-open alert to every device of the store's user that has it on, and waits. It reports
// how many sends succeeded; an error means none did.
func (s *Service) DeliverLeftOpen(ctx context.Context, store *Store, model string, kinds []string, minutes int) (int, error) {
	if s == nil || store == nil {
		return 0, errors.New("push: not configured")
	}
	subs, err := store.List(ctx)
	if err != nil {
		return 0, err
	}
	p := LeftOpenPayload(model, kinds, minutes)
	var ok int
	var last error
	for _, sub := range subs {
		if !selectLeftOpen(sub) {
			continue
		}
		if err := s.sendOne(ctx, store, sub, p); err != nil {
			last = err
			continue
		}
		ok++
	}
	ok += s.fanAPNs(ctx, store, p)
	if ok == 0 {
		if last == nil {
			last = errors.New("push: no device has the left-open alert on")
		}
		return 0, last
	}
	return ok, nil
}

// DeliverMilestone sends a milestone push to every subscription of the store's
// user that has milestones on, and waits. It reports how many sends succeeded;
// an error means none did.
func (s *Service) DeliverMilestone(ctx context.Context, store *Store, title, body string) (int, error) {
	if s == nil || store == nil {
		return 0, errors.New("push: not configured")
	}
	subs, err := store.List(ctx)
	if err != nil {
		return 0, err
	}
	p := Payload{Title: title, Body: body, Tag: "rivolt-milestone", URL: "/", Kind: "milestone"}
	var ok int
	var last error
	for _, sub := range subs {
		if !sub.OnMilestones {
			continue
		}
		if err := s.sendOne(ctx, store, sub, p); err != nil {
			last = err
			continue
		}
		ok++
	}
	ok += s.fanAPNs(ctx, store, p)
	if ok == 0 {
		if last == nil {
			last = errors.New("push: no subscription has milestones on")
		}
		return 0, last
	}
	return ok, nil
}

// TripRecapPayload is the saved-trip recap: generic title (the trip's name never goes in a
// push; it shows in the app after the tap), planned and actual arrival charge and cost
// in the body, a deep link to the day-by-day.
func TripRecapPayload(body, savedTripID string) Payload {
	return Payload{
		Title: "You drove a saved trip",
		Body:  body,
		Tag:   "rivolt-trip-recap",
		URL:   "/plan?trip=" + savedTripID + "&tab=pva",
		Kind:  "trip_recap",
	}
}

// DeliverTripRecap sends the trip recap to the user's browsers and app installs that
// have notifications on, and waits. The opt-in is the account's (Settings, Trip recap),
// checked by the caller, not a per-device switch. It reports how many sends succeeded; an
// error means none did.
func (s *Service) DeliverTripRecap(ctx context.Context, store *Store, p Payload) (int, error) {
	if s == nil || store == nil {
		return 0, errors.New("push: not configured")
	}
	subs, err := store.List(ctx)
	if err != nil {
		return 0, err
	}
	var ok int
	var last error
	for _, sub := range subs {
		if err := s.sendOne(ctx, store, sub, p); err != nil {
			last = err
			continue
		}
		ok++
	}
	ok += s.fanAPNsTo(ctx, store, p, true)
	if ok == 0 {
		if last == nil {
			last = errors.New("push: no device has notifications on")
		}
		return 0, last
	}
	return ok, nil
}

// NotifyAnomaly fires when an anomaly is detected for the user. Scoped
// to the user via store.
func (s *Service) NotifyAnomaly(store *Store, title, body, deepLink string) {
	if s == nil || store == nil {
		return
	}
	go s.broadcast(store, Payload{
		Title: title,
		Body:  body,
		Tag:   "rivolt-anomaly",
		URL:   deepLink,
		Kind:  "anomaly",
	}, selectAnomaly)
}

// NotifySoftwareUpdate fires when a vehicle is offered a new software
// version (installed=false) or its installed version changed
// (installed=true). Scoped to the user via store. The tap opens the app: Rivian's
// release-notes link is a one-hour signed URL, so it is never put in a payload.
func (s *Service) NotifySoftwareUpdate(store *Store, vehicle, version string, installed bool) {
	if s == nil || store == nil || version == "" {
		return
	}
	p := softwareUpdatePayload(vehicle, version, installed)
	go s.broadcast(store, p, selectSoftwareUpdate)
}

func softwareUpdatePayload(vehicle, version string, installed bool) Payload {
	if vehicle == "" {
		vehicle = "your vehicle"
	} else {
		vehicle = "your " + vehicle
	}
	p := Payload{Tag: "rivolt-software-update", URL: "/", Kind: "software_update"}
	if installed {
		p.Title = "Software updated"
		p.Body = fmt.Sprintf("%s is now on %s.", capitalize(vehicle), version)
	} else {
		p.Title = "Software update available"
		p.Body = fmt.Sprintf("Update %s available for %s.", version, vehicle)
	}
	return p
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// SendTest delivers a single test notification to one endpoint. The
// store must be scoped to the requesting user so we don't leak test
// pings across tenants.
func (s *Service) SendTest(ctx context.Context, store *Store, endpoint string) error {
	if store == nil {
		return errors.New("push: nil store")
	}
	subs, err := store.List(ctx)
	if err != nil {
		return err
	}
	for _, sub := range subs {
		if sub.Endpoint == endpoint {
			return s.sendOne(ctx, store, sub, Payload{
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
func selectSoftwareUpdate(s Subscription) bool { return s.OnSoftwareUpdate }
func selectLeftOpen(s Subscription) bool       { return s.OnLeftOpen }

// broadcast fans a payload out to every stored subscription matching
// the selector. Each send is independent: one bad subscription can't
// block the others, and 404/410 responses purge the dead subscription.
// store must be scoped to a single user — broadcasts are user-private.
func (s *Service) broadcast(store *Store, p Payload, sel selector) {
	// Deliberately use a fresh context independent of any request-scoped
	// one: hooks running this typically outlive the triggering frame.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// The iPhone app's installs, independently of the web subscriptions (a user
	// may have only the app).
	var apnsDone sync.WaitGroup
	if s.apns != nil {
		apnsDone.Add(1)
		go func() {
			defer apnsDone.Done()
			s.fanAPNs(ctx, store, p)
		}()
	}
	defer apnsDone.Wait()

	subs, err := store.List(ctx)
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
			if err := s.sendOne(ctx, store, sub, p); err != nil {
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
// service reports the subscription is gone (404/410) we purge it via
// the supplied per-user store so the DB doesn't accumulate dead rows.
func (s *Service) sendOne(ctx context.Context, store *Store, sub Subscription, p Payload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	// An endpoint stored before the check existed (or tampered with) is not sent to.
	if err := ValidEndpoint(sub.Endpoint); err != nil {
		s.logger.Warn("push: refusing a stored endpoint", "host", endpointHost(sub.Endpoint))
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
	//
	// webpush-go prepends "mailto:" itself unless the subscriber
	// already starts with "https:" — passing the full mailto: URL
	// produces a doubled "mailto:mailto:..." which APNs rejects as
	// BadJwtToken. Strip a leading "mailto:" so we hand the library
	// what it expects (a bare email address).
	subscriber := strings.TrimPrefix(s.vapid.Subject, "mailto:")
	res, err := s.sender.SendNotification(body, wsub, &webpush.Options{
		Subscriber:      subscriber,
		VAPIDPublicKey:  s.vapid.PublicKey,
		VAPIDPrivateKey: s.vapid.PrivateKey,
		TTL:             s.ttlSeconds,
		Urgency:         webpush.UrgencyHigh,
		HTTPClient:      pushHTTPClient,
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
		// (uninstalled PWA, revoked permission, etc.). Clean it up via
		// the per-user store if the caller supplied one; otherwise log
		// and move on (the row will be cleaned up on a later attempt).
		if store != nil {
			if delErr := store.Delete(ctx, sub.Endpoint); delErr != nil {
				s.logger.Warn("push: purge dead sub failed", "err", delErr.Error())
			} else {
				s.logger.Info("push: purged dead subscription",
					"host", host,
					"status", res.StatusCode,
					"body", previewBody(respBody))
			}
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

// Error never carries the push service's response body: the message reaches the
// API (the test-notification route) and a body from an unexpected host is not for
// the caller. The body is logged server-side where it was received.
func (e *sendError) Error() string {
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
