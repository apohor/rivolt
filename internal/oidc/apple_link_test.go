package oidc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// "Link Apple to my account": the callback's link mode (design: _shared/apple-link-design.md, DA's conditions A–D).

// fakeIntents is AppleLinkIntents over a map; expired makes every Consume refuse (and still spend the request).
type fakeIntents struct {
	m       map[string]AppleLinkIntent
	expired bool
}

func (f *fakeIntents) Create(_ context.Context, uid uuid.UUID, stateHash, sessionHash []byte, _ time.Time) error {
	f.m[string(stateHash)] = AppleLinkIntent{UserID: uid, SessionHash: sessionHash}
	return nil
}

func (f *fakeIntents) Consume(_ context.Context, stateHash []byte, _ time.Time) (AppleLinkIntent, bool, error) {
	in, ok := f.m[string(stateHash)]
	delete(f.m, string(stateHash))
	return in, ok && !f.expired, nil
}

var (
	linkUser    = uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	linkSession = uuid.MustParse("00000000-0000-0000-0000-0000000000b1")
)

// beginLink is what the authenticated start does: BeginAppleLink for linkUser from linkSession. It returns the Apple state cookie and the
// authorize URL, and primes the fake Apple with the flow's nonce and challenge.
func (h *appleHarness) beginLink() (*http.Cookie, *url.URL) {
	h.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/oidc/apple/link/start", nil)
	authURL, err := h.svc.BeginAppleLink(rec, req, linkUser, linkSession)
	if err != nil {
		h.t.Fatal(err)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != appleStateCookie {
		h.t.Fatalf("link start cookies = %+v, want only %s", cookies, appleStateCookie)
	}
	loc, err := url.Parse(authURL)
	if err != nil {
		h.t.Fatal(err)
	}
	h.fa.mu.Lock()
	h.fa.nonce = loc.Query().Get("nonce")
	h.fa.challenge = loc.Query().Get("code_challenge")
	h.fa.claims = map[string]any{"sub": testAppleSub, "email": "hidden@privaterelay.appleid.com", "email_verified": "true", "is_private_email": "true"}
	h.fa.mu.Unlock()
	return cookies[0], loc
}

func (h *appleHarness) linkCallback(c *http.Cookie, loc *url.URL) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.callback(c, url.Values{"code": {"the-code"}, "state": {loc.Query().Get("state")}})
}

func location(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	return rec.Header().Get("Location")
}

func TestLinkHashMatchesTheStoresAndTheSessionIDsText(t *testing.T) {
	// internal/appleid.Hash is the same sha256; the hex of this value is pinned there too (store_integration_test).
	if got := LinkHash("abc"); len(got) != 32 || got[0] != 0xba || got[1] != 0x78 {
		t.Fatalf("LinkHash(abc) = %x, want sha256 (ba7816bf…)", got)
	}
}

func TestBeginAppleLinkRecordsTheIntentAndSetsTheAppleCookie(t *testing.T) {
	h := newAppleHarness(t)
	c, loc := h.beginLink()
	if c.SameSite != http.SameSiteNoneMode || !c.Secure || !c.HttpOnly || c.Path != "/api/auth/oidc/apple" || c.Domain != "" || c.MaxAge <= 0 || c.MaxAge > 600 {
		t.Errorf("cookie = %+v; want the Apple state cookie rules", c)
	}
	in, ok := h.intents.m[string(LinkHash(loc.Query().Get("state")))]
	if !ok || in.UserID != linkUser || string(in.SessionHash) != string(LinkHash(linkSession.String())) {
		t.Errorf("intent = %+v, %v; want linkUser from linkSession, keyed by the state's hash", in, ok)
	}
	if loc.Query().Get("response_mode") != "form_post" || loc.Query().Get("nonce") != loc.Query().Get("state") {
		t.Errorf("authorize URL = %s", loc)
	}
}

func TestBeginAppleLinkNeedsTheLinkPlumbing(t *testing.T) {
	h := newAppleHarness(t)
	h.svc.linkIntents = nil
	rec := httptest.NewRecorder()
	if _, err := h.svc.BeginAppleLink(rec, httptest.NewRequest(http.MethodPost, "/", nil), linkUser, linkSession); err != ErrAppleNotConfigured {
		t.Errorf("err = %v, want ErrAppleNotConfigured", err)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a cookie was set without a recorded request")
	}
}

// A Hide My Email identity links to the signed-in account: no session, no account lookup, no sign-up, nothing of the address kept.
func TestLinkModeAttachesTheIdentityToTheIntentsAccount(t *testing.T) {
	h := newAppleHarness(t)
	c, loc := h.beginLink()
	rec := h.linkCallback(c, loc)
	if got := location(t, rec); got != "/settings?apple=linked" {
		t.Fatalf("Location = %q, want /settings?apple=linked", got)
	}
	if len(h.links.linked) != 1 || !strings.HasPrefix(h.links.linked[0], linkUser.String()+" "+testAppleSub+" ") {
		t.Errorf("linked = %v, want the intent's account and the sub", h.links.linked)
	}
	if len(h.issued) != 0 || len(h.ensured) != 0 {
		t.Errorf("link mode issued sessions %v or ensured users %v", h.issued, h.ensured)
	}
	if !clearsAppleCookie(rec) {
		t.Error("the Apple state cookie was not cleared")
	}
	if len(h.intents.m) != 0 {
		t.Error("the intent was not spent")
	}
	if len(h.outcomes) != 1 || h.outcomes[0] != "linked" {
		t.Errorf("outcomes = %v, want [linked]", h.outcomes)
	}
	h.noAppleIDsInLogs("privaterelay")
}

func TestLinkModeRefusesAnAppleIDOfAnotherAccount(t *testing.T) {
	h := newAppleHarness(t)
	h.links.uidBySub[testAppleSub] = uuid.MustParse("00000000-0000-0000-0000-0000000000c9")
	c, loc := h.beginLink()
	if got := location(t, h.linkCallback(c, loc)); got != "/settings?apple=apple_in_use" {
		t.Errorf("Location = %q", got)
	}
	if len(h.links.linked) != 0 || len(h.outcomes) != 1 || h.outcomes[0] != "in_use" {
		t.Errorf("linked %v, outcomes %v", h.links.linked, h.outcomes)
	}
}

func TestLinkModeRelinksTheSameIdentity(t *testing.T) {
	h := newAppleHarness(t)
	h.links.uidBySub[testAppleSub] = linkUser
	c, loc := h.beginLink()
	if got := location(t, h.linkCallback(c, loc)); got != "/settings?apple=linked" || len(h.links.linked) != 1 {
		t.Errorf("Location = %q, linked %v", got, h.links.linked)
	}
}

func TestLinkModeRefusesAnAccountLinkedToAnotherAppleID(t *testing.T) {
	h := newAppleHarness(t)
	h.links.linkErr = ErrAppleLinkConflict
	c, loc := h.beginLink()
	if got := location(t, h.linkCallback(c, loc)); got != "/settings?apple=apple_linked" {
		t.Errorf("Location = %q", got)
	}
	if len(h.outcomes) != 1 || h.outcomes[0] != "account_linked" {
		t.Errorf("outcomes = %v", h.outcomes)
	}
}

// DA's condition A: a link started by one session is not completed after that session ended (a shared browser).
func TestLinkModeRefusesWhenTheStartingSessionHasEnded(t *testing.T) {
	h := newAppleHarness(t)
	c, loc := h.beginLink()
	h.live = false
	if got := location(t, h.linkCallback(c, loc)); got != "/settings?apple=session_expired" {
		t.Errorf("Location = %q", got)
	}
	if len(h.links.linked) != 0 || len(h.issued) != 0 {
		t.Errorf("linked %v, issued %v", h.links.linked, h.issued)
	}
}

// A request that expired (or was spent) never turns into an ordinary sign-in as the Apple account, which would swap the user's session.
func TestAnExpiredLinkRequestIsNeverASignIn(t *testing.T) {
	h := newAppleHarness(t)
	c, loc := h.beginLink()
	h.intents.expired = true
	if got := location(t, h.linkCallback(c, loc)); got != "/settings?apple=session_expired" {
		t.Errorf("Location = %q, want a refusal back at Settings", got)
	}
	if len(h.issued) != 0 || len(h.ensured) != 0 || len(h.links.linked) != 0 {
		t.Errorf("issued %v, ensured %v, linked %v", h.issued, h.ensured, h.links.linked)
	}
}

// DA's condition D: a valid request with the wrong or no state cookie is refused and the request is spent, so a second try with the right
// cookie cannot link either.
func TestLinkModeWithAnotherFlowsCookieIsRefusedAndSpendsTheRequest(t *testing.T) {
	h := newAppleHarness(t)
	goodCookie, loc := h.beginLink()
	otherCookie, _ := h.start() // an ordinary sign-in's cookie: a different state
	if got := location(t, h.linkCallback(otherCookie, loc)); got != "/settings?apple=session_expired" {
		t.Errorf("Location = %q", got)
	}
	if len(h.intents.m) != 0 {
		t.Error("the refused attempt did not spend the request")
	}
	h.fa.mu.Lock()
	h.fa.nonce = loc.Query().Get("nonce")
	h.fa.challenge = loc.Query().Get("code_challenge")
	h.fa.mu.Unlock()
	if got := location(t, h.linkCallback(goodCookie, loc)); !strings.HasPrefix(got, "/settings?apple=") || got == "/settings?apple=linked" {
		t.Errorf("second try Location = %q, must not link", got)
	}
	if len(h.links.linked) != 0 || len(h.issued) != 0 {
		t.Errorf("linked %v, issued %v", h.links.linked, h.issued)
	}
}

func TestLinkModeWithNoCookieIsRefusedAndSpendsTheRequest(t *testing.T) {
	h := newAppleHarness(t)
	_, loc := h.beginLink()
	if got := location(t, h.linkCallback(nil, loc)); got != "/settings?apple=session_expired" {
		t.Errorf("Location = %q", got)
	}
	if len(h.intents.m) != 0 || len(h.links.linked) != 0 {
		t.Errorf("intents left %d, linked %v", len(h.intents.m), h.links.linked)
	}
}

// An ordinary sign-in is untouched by link mode: no request for its state, so it signs in as before.
func TestSignInIsUnchangedByLinkMode(t *testing.T) {
	h := newAppleHarness(t)
	rec := h.signIn(map[string]any{"sub": testAppleSub, "email": "me@example.com", "email_verified": "true"}, "")
	if len(h.issued) != 1 || strings.Contains(location(t, rec), "/settings") {
		t.Errorf("issued %v, Location %q", h.issued, rec.Header().Get("Location"))
	}
}

// DA's condition B: an Apple ID linked to an account (here by the link flow) signs in to THAT account when it hides its email, never to
// a new one.
func TestALinkedRelayIdentitySignsIntoTheLinkedAccountNotANewOne(t *testing.T) {
	h := newAppleHarness(t)
	c, loc := h.beginLink()
	if got := location(t, h.linkCallback(c, loc)); got != "/settings?apple=linked" {
		t.Fatalf("link: Location = %q", got)
	}
	// The store now answers for the sub (the fake records the link; mirror what the real store does).
	h.links.bySub[testAppleSub] = "owner@example.com"
	h.existing["owner@example.com"] = true
	rec := h.signIn(map[string]any{"sub": testAppleSub, "email": "hidden@privaterelay.appleid.com", "email_verified": "true", "is_private_email": "true"}, "")
	if got := location(t, rec); got != "/" {
		t.Fatalf("sign-in: Location = %q, want /", got)
	}
	if len(h.ensured) != 1 || h.ensured[0].username != "owner@example.com" || h.ensured[0].email != "" {
		t.Errorf("ensured = %+v, want only the linked account, with no email taken from Apple", h.ensured)
	}
	for _, e := range h.ensured {
		if strings.Contains(e.username, "privaterelay") || strings.Contains(e.email, "privaterelay") {
			t.Errorf("the relay address reached the account: %+v", e)
		}
	}
}
