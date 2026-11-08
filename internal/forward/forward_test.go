package forward

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func testSigner(t *testing.T) *Signer {
	t.Helper()
	s, err := NewSigner(bytes.Repeat([]byte{7}, KeyLen))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func goodRequest(t *testing.T, s *Signer, kind Kind, args string) *Request {
	t.Helper()
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	r := &Request{V: Version, ID: id, User: "11111111-2222-3333-4444-555555555555", Group: "11111111-2222-3333-4444-555555555555", Epoch: 7,
		Kind: kind, Args: json.RawMessage(args), ExpiresMS: 1_700_000_035_000}
	s.SignRequest(r)
	return r
}

// T1: the allowlist and the per-kind schemas.
func TestAllowlistAndSchemas(t *testing.T) {
	s := testSigner(t)
	plan := `{"vehicle_id":"v-fake-1","starting_soc":80,"starting_range_meters":0,"origin_bearing":0,"waypoints":[{"lat":34.1,"lon":-118.1},{"lat":35.2,"lon":-117.0}],"drive_mode":"SPORT"}`
	for _, c := range []struct {
		name string
		kind Kind
		args string
		ok   bool
	}{
		{"plan_legs", KindPlanLegs, plan, true},
		{"fresh_state", KindFreshState, `{"vehicle_id":"v-1","max_age_ms":30000}`, true},
		{"adopt", KindAdopt, `{}`, true},
		{"ota_notes", KindOTANotes, `{"vehicle_id":"v-1","release_id":"r.2026.40"}`, true},
		{"unknown kind", Kind("exec"), `{}`, false},
		{"empty kind", Kind(""), `{}`, false},
		{"adopt may not carry a credential", KindAdopt, `{"token":"x"}`, false},
		{"adopt may not carry a password", KindAdopt, `{"password":"x"}`, false},
		{"adopt takes no args at all", KindAdopt, `{"user":"x"}`, false},
		{"unknown field", KindFreshState, `{"vehicle_id":"v-1","max_age_ms":1,"extra":1}`, false},
		{"wrong type", KindFreshState, `{"vehicle_id":5,"max_age_ms":1}`, false},
		{"vehicle id charset", KindFreshState, `{"vehicle_id":"v/../1","max_age_ms":1}`, false},
		{"vehicle id too long", KindFreshState, `{"vehicle_id":"` + strings.Repeat("a", 65) + `","max_age_ms":1}`, false},
		{"empty vehicle id", KindFreshState, `{"vehicle_id":"","max_age_ms":1}`, false},
		{"negative max age", KindFreshState, `{"vehicle_id":"v","max_age_ms":-1}`, false},
		{"huge max age", KindFreshState, `{"vehicle_id":"v","max_age_ms":999999999}`, false},
		{"latitude out of range", KindPlanLegs, strings.Replace(plan, `"lat":34.1`, `"lat":91`, 1), false},
		{"longitude out of range", KindPlanLegs, strings.Replace(plan, `"lon":-118.1`, `"lon":-181`, 1), false},
		{"soc out of range", KindPlanLegs, strings.Replace(plan, `"starting_soc":80`, `"starting_soc":101`, 1), false},
		{"one waypoint", KindPlanLegs, `{"vehicle_id":"v","starting_soc":50,"starting_range_meters":0,"origin_bearing":0,"waypoints":[{"lat":1,"lon":2}]}`, false},
		{"eleven waypoints", KindPlanLegs, `{"vehicle_id":"v","starting_soc":50,"starting_range_meters":0,"origin_bearing":0,"waypoints":[` + strings.TrimSuffix(strings.Repeat(`{"lat":1,"lon":2},`, 11), ",") + `]}`, false},
		{"bad drive mode", KindPlanLegs, strings.Replace(plan, `SPORT`, `LUDICROUS`, 1), false},
		{"not JSON", KindPlanLegs, `nope`, false},
		{"trailing data", KindFreshState, `{"vehicle_id":"v","max_age_ms":1} {"x":1}`, false},
		{"null args", KindPlanLegs, `null`, false},
		{"empty args", KindFreshState, ``, false},
	} {
		r := goodRequest(t, s, c.kind, c.args)
		_, err := Check(s, r)
		if (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
	// Every kind in the allowlist has a positive timeout and a validator.
	for k, spec := range Kinds {
		if spec.Timeout <= 0 || spec.Validate == nil {
			t.Errorf("kind %s has no timeout or validator", k)
		}
	}
	if Kinds[KindAdopt].Retry {
		t.Error("adopt is best effort and never retried")
	}
	// The longest kind timeout fits inside the request list's lifetime with slack.
	for k, spec := range Kinds {
		if spec.Timeout+10*time.Second > RequestListTTL*time.Second {
			t.Errorf("kind %s: timeout %v leaves less than 10 s inside the %d s list TTL", k, spec.Timeout, RequestListTTL)
		}
	}
}

// T1/T12: the signature is checked before the content; every field is covered; a re-spaced args still verifies; another key and a
// reply signed as a request do not.
func TestSignatures(t *testing.T) {
	s := testSigner(t)
	r := goodRequest(t, s, KindFreshState, `{"vehicle_id":"v-1","max_age_ms":30000}`)
	if err := s.VerifyRequest(r); err != nil {
		t.Fatalf("a signed request must verify: %v", err)
	}
	// Whitespace in args does not matter (the digest is of the compacted JSON).
	sp := *r
	sp.Args = json.RawMessage("{ \"vehicle_id\" : \"v-1\",\n \"max_age_ms\":30000 }")
	if err := s.VerifyRequest(&sp); err != nil {
		t.Errorf("a re-spaced copy of the same args must verify: %v", err)
	}
	for name, mut := range map[string]func(*Request){
		"v":       func(r *Request) { r.V = 2 },
		"id":      func(r *Request) { r.ID = strings.Repeat("a", 32) },
		"user":    func(r *Request) { r.User = "someone-else" },
		"group":   func(r *Request) { r.Group = "another-group" },
		"epoch":   func(r *Request) { r.Epoch++ },
		"kind":    func(r *Request) { r.Kind = KindAdopt },
		"args":    func(r *Request) { r.Args = json.RawMessage(`{"vehicle_id":"v-2","max_age_ms":30000}`) },
		"expires": func(r *Request) { r.ExpiresMS++ },
		"sig":     func(r *Request) { r.Sig = strings.Repeat("0", 64) },
		"nosig":   func(r *Request) { r.Sig = "" },
		"badhex":  func(r *Request) { r.Sig = "zz" },
	} {
		c := *r
		mut(&c)
		if err := s.VerifyRequest(&c); !errors.Is(err, ErrBadSig) {
			t.Errorf("a changed %s must fail verification, got %v", name, err)
		}
	}
	// Another install's key.
	other, _ := NewSigner(bytes.Repeat([]byte{9}, KeyLen))
	if err := other.VerifyRequest(r); !errors.Is(err, ErrBadSig) {
		t.Error("a request signed by another key must not verify")
	}
	// Check refuses a bad signature before it looks at the kind or the args: an unknown kind with a bad signature is a signature error.
	bad := *r
	bad.Kind, bad.Sig = Kind("exec"), "00"
	if _, err := Check(s, &bad); !errors.Is(err, ErrBadSig) {
		t.Errorf("signature is checked first, got %v", err)
	}
	// Domain separation: a reply's signature is not a request's, and the reverse.
	rep := &Reply{V: Version, ID: r.ID, User: r.User, OK: true, Status: 200, Body: json.RawMessage(`{"a":1}`)}
	s.SignReply(rep)
	if err := s.VerifyReply(rep); err != nil {
		t.Fatalf("a signed reply must verify: %v", err)
	}
	asReq := *r
	asReq.Sig = rep.Sig
	if err := s.VerifyRequest(&asReq); !errors.Is(err, ErrBadSig) {
		t.Error("a reply signature must not verify as a request signature")
	}
	reqAsRep := *rep
	reqAsRep.Sig = r.Sig
	if err := s.VerifyReply(&reqAsRep); !errors.Is(err, ErrBadSig) {
		t.Error("a request signature must not verify as a reply signature")
	}
	for name, mut := range map[string]func(*Reply){
		"id": func(x *Reply) { x.ID = strings.Repeat("b", 32) }, "user": func(x *Reply) { x.User = "x" }, "ok": func(x *Reply) { x.OK = false },
		"status": func(x *Reply) { x.Status = 500 }, "class": func(x *Reply) { x.Class = "upstream" }, "body": func(x *Reply) { x.Body = json.RawMessage(`{"a":2}`) },
		"v": func(x *Reply) { x.V = 9 },
	} {
		c := *rep
		mut(&c)
		if err := s.VerifyReply(&c); !errors.Is(err, ErrBadSig) {
			t.Errorf("a changed reply %s must fail verification, got %v", name, err)
		}
	}
	// Fields cannot bleed into each other: ("ab","c") and ("a","bc") sign differently.
	if mac([]byte("k"), "ab", "c") == mac([]byte("k"), "a", "bc") {
		t.Error("the signed fields must be length-prefixed")
	}
}

func TestKeyParsing(t *testing.T) {
	good := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, KeyLen))
	if _, err := ParseKey(good); err != nil {
		t.Errorf("a 32-byte base64 key must parse: %v", err)
	}
	if _, err := ParseKey(" " + good + "\n"); err != nil {
		t.Errorf("surrounding whitespace is trimmed: %v", err)
	}
	if _, err := ParseKey(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 48))); err != nil {
		t.Errorf("a key of more than 32 bytes is valid (at least 32): %v", err)
	}
	for _, bad := range []string{"", "not base64!!", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 31)), base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16))} {
		_, err := ParseKey(bad)
		if !errors.Is(err, ErrBadKey) {
			t.Errorf("%q: err=%v, want ErrBadKey", bad, err)
		} else if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("the error must not echo the key")
		}
	}
	if _, err := NewSigner(make([]byte, 16)); !errors.Is(err, ErrBadKey) {
		t.Error("a short key must be refused")
	}
}

// T2: ids are 128 random bits and a reply key cannot be named by a request.
func TestIDsAndKeys(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		id, err := NewID()
		if err != nil || !ValidID(id) {
			t.Fatalf("id %q err=%v", id, err)
		}
		if seen[id] {
			t.Fatal("duplicate id")
		}
		seen[id] = true
	}
	for _, bad := range []string{"", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.Repeat("A", 32), strings.Repeat("g", 32), "rivolt:rl:" + strings.Repeat("a", 22), strings.Repeat("a", 31) + ":", strings.Repeat("a", 31) + "\n"} {
		if ValidID(bad) {
			t.Errorf("%q must not be a valid id", bad)
		}
	}
	k := Keys{}
	id := strings.Repeat("a", 32)
	for name, key := range map[string]string{"queue": k.Queue("pod-1"), "ready": k.Ready("pod-1"), "reply": k.Reply(id), "userset": k.UserSet("11111111-2222-3333-4444-555555555555")} {
		if !strings.HasPrefix(key, "rivolt:") {
			t.Errorf("%s key %q must start with rivolt: (the Valkey user's key pattern)", name, key)
		}
	}
	if k.Queue("pod-1") == k.Ready("pod-1") || k.Reply(id) == k.UserSet(id) {
		t.Error("keys of different roles must differ")
	}
	if (Keys{Prefix: "rivolt-preview"}).Queue("p") != "rivolt-preview:fwd:p" {
		t.Error("a prefix is used when set")
	}
	if !ValidPod("rivolt-5d4f64c58c-nfmdm") || ValidPod("a:b") || ValidPod("") || ValidPod("a b") || ValidPod(strings.Repeat("a", 129)) {
		t.Error("pod id charset")
	}
}

// T3: expiry is arithmetic on the Redis clock; the local clock plays no part.
func TestExpiryUsesTheRedisClock(t *testing.T) {
	now := int64(1_700_000_000_000)
	exp := ExpiresMS(now, KindPlanLegs)
	if exp != now+28_000 {
		t.Fatalf("plan_legs expires 28 s after Redis time, got %d", exp-now)
	}
	if Expired(now+27_999, exp) || !Expired(now+28_000, exp) || !Expired(now+99_000, exp) {
		t.Error("an item is expired at its expiry, not before")
	}
	if ExpiresMS(now, KindFreshState)-now != 10_000 || ExpiresMS(now, KindOTANotes)-now != 15_000 {
		t.Error("per-kind timeouts")
	}
}

// T6 (shape): size caps and strict decoding of messages.
func TestDecodeCapsAndStrictness(t *testing.T) {
	s := testSigner(t)
	r := goodRequest(t, s, KindAdopt, `{}`)
	b, _ := json.Marshal(r)
	if got, err := DecodeRequest(b); err != nil || got.ID != r.ID {
		t.Fatalf("a good request decodes: %v", err)
	}
	if _, err := DecodeRequest(append(bytes.Repeat([]byte{' '}, MaxRequestBytes), b...)); !errors.Is(err, ErrTooBig) {
		t.Error("an oversize request is refused")
	}
	for name, raw := range map[string]string{
		"unknown field": strings.Replace(string(b), `"v":1`, `"v":1,"extra":true`, 1),
		"wrong version": strings.Replace(string(b), `"v":1`, `"v":2`, 1),
		"bad id":        strings.Replace(string(b), r.ID, "short", 1),
		"trailing":      string(b) + `{}`,
		"not json":      "garbage",
	} {
		if _, err := DecodeRequest([]byte(raw)); err == nil {
			t.Errorf("%s: a malformed request must be refused", name)
		}
	}
	rep := &Reply{V: Version, ID: r.ID, User: r.User, OK: true, Status: 200, Body: json.RawMessage(`{}`)}
	s.SignReply(rep)
	rb, _ := json.Marshal(rep)
	if got, err := DecodeReply(rb); err != nil || got.ID != r.ID {
		t.Fatalf("a good reply decodes: %v", err)
	}
	if _, err := DecodeReply(append(bytes.Repeat([]byte{' '}, MaxReplyBytes), rb...)); !errors.Is(err, ErrTooBig) {
		t.Error("an oversize reply is refused")
	}
	if _, err := DecodeReply([]byte(strings.Replace(string(rb), `"v":1`, `"v":1,"x":1`, 1))); err == nil {
		t.Error("a reply with an unknown field is refused")
	}
}

// The metrics: every (kind, outcome) series exists at 0 from the first scrape, and no label names a user, vehicle or group.
func TestMetricsAreZeroFilled(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	want := (len(Kinds) + 1) * len(Outcomes) // every kind plus "unknown" (undecodable or unknown-kind messages)
	if got := testutil.CollectAndCount(m.Total); got != want {
		t.Errorf("forward_total series = %d, want %d (every kind x outcome at 0)", got, want)
	}
	if got := testutil.CollectAndCount(m.Seconds); got != len(Kinds)+1 {
		t.Errorf("forward_seconds series = %d, want %d", got, len(Kinds)+1)
	}
	m.Total.WithLabelValues(string(KindPlanLegs), "ok").Inc()
	if testutil.ToFloat64(m.Total.WithLabelValues(string(KindPlanLegs), "ok")) != 1 {
		t.Error("counter increments")
	}
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		for _, mt := range mf.GetMetric() {
			for _, l := range mt.GetLabel() {
				if n := l.GetName(); n != "kind" && n != "outcome" && n != "le" {
					t.Errorf("metric %s has label %q: only kind and outcome are allowed", mf.GetName(), n)
				}
			}
		}
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatal(err)
	}
	// A second registration on the same registry is a programming error, not a silent duplicate.
	defer func() {
		if recover() == nil {
			t.Error("registering twice must panic")
		}
	}()
	NewMetrics(reg)
}
