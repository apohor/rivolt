package push

import (
	"fmt"
	"net/url"
	"strings"
)

// ObserveSends registers fn to be told the outcome of every send to a push service: channel "web" or "apns", the service
// (HostClass of the endpoint; "apple" for the app's APNs) and the outcome (ok, gone, rejected, error). It carries no endpoint,
// user or token. Chainable; call before the service is used.
func (s *Service) ObserveSends(fn func(channel, service, outcome string)) *Service {
	s.observe = fn
	return s
}

func (s *Service) observeSend(channel, endpoint, outcome string) {
	if s == nil || s.observe == nil {
		return
	}
	class := "apple"
	if channel == "web" {
		class = HostClass(endpoint)
	}
	s.observe(channel, class, outcome)
}

// HostClass names the push service behind an endpoint, from a fixed set so it is safe as a metric label.
func HostClass(endpoint string) string {
	h := EndpointHost(endpoint)
	switch {
	case strings.HasSuffix(h, "push.apple.com"):
		return "apple"
	case h == "fcm.googleapis.com" || h == "jmt17.google.com":
		return "fcm"
	case strings.HasSuffix(h, "push.services.mozilla.com"):
		return "mozilla"
	case strings.HasSuffix(h, ".notify.windows.com"):
		return "windows"
	}
	return "other"
}

// CheckVAPIDSubject reports why a VAPID subject would be refused by Apple's web push service, which only accepts a mailto: address
// or an https URL on a real domain in the JWT "sub" claim; a reserved name (.invalid, example.*, localhost) is answered with 403
// BadJwtToken. nil when it looks deliverable.
func CheckVAPIDSubject(subject string) error {
	var host string
	switch {
	case strings.HasPrefix(subject, "mailto:"):
		addr := strings.TrimPrefix(subject, "mailto:")
		i := strings.LastIndex(addr, "@")
		if i <= 0 || i == len(addr)-1 {
			return fmt.Errorf("vapid subject: mailto: needs user@domain")
		}
		host = addr[i+1:]
	case strings.HasPrefix(subject, "https://"):
		u, err := url.Parse(subject)
		if err != nil || u.Hostname() == "" {
			return fmt.Errorf("vapid subject: not a valid https URL")
		}
		host = u.Hostname()
	default:
		return fmt.Errorf("vapid subject: must be a mailto: address or an https URL")
	}
	host = strings.ToLower(host)
	last := host[strings.LastIndex(host, ".")+1:]
	if !strings.Contains(host, ".") || last == "invalid" || last == "local" || last == "localhost" || last == "test" || last == "example" ||
		host == "example.com" || host == "example.org" || host == "example.net" || strings.HasSuffix(host, ".example.com") {
		return fmt.Errorf("vapid subject: %q is a reserved name that Apple rejects", host)
	}
	return nil
}
