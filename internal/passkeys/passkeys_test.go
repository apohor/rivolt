package passkeys

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/passkeys/passkeytest"
)

const (
	testRP     = "rivolt.dev"
	testOrigin = "https://rivolt.dev"
)

type fakeStore struct {
	mu         sync.Mutex
	handles    map[uuid.UUID][]byte
	creds      []Credential
	ceremonies map[uuid.UUID]Ceremony
}

func newFakeStore() *fakeStore {
	return &fakeStore{handles: map[uuid.UUID][]byte{}, ceremonies: map[uuid.UUID]Ceremony{}}
}

func (f *fakeStore) UserHandle(_ context.Context, uid uuid.UUID) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h, ok := f.handles[uid]; ok {
		return h, nil
	}
	f.handles[uid] = NewHandle()
	return f.handles[uid], nil
}
func (f *fakeStore) UserByHandle(_ context.Context, handle []byte) (uuid.UUID, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for u, h := range f.handles {
		if bytes.Equal(h, handle) {
			return u, true, nil
		}
	}
	return uuid.Nil, false, nil
}
func (f *fakeStore) Credentials(_ context.Context, uid uuid.UUID) ([]Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Credential
	for _, c := range f.creds {
		if c.UserID == uid {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *fakeStore) CredentialByID(_ context.Context, id []byte) (Credential, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.creds {
		if bytes.Equal(c.CredentialID, id) {
			return c, true, nil
		}
	}
	return Credential{}, false, nil
}
func (f *fakeStore) Add(_ context.Context, c Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds = append(f.creds, c)
	return nil
}
func (f *fakeStore) RecordUse(_ context.Context, id []byte, n uint32, bs bool, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.creds {
		if bytes.Equal(f.creds[i].CredentialID, id) {
			f.creds[i].SignCount, f.creds[i].BackupState, f.creds[i].LastUsedAt = n, bs, &at
		}
	}
	return nil
}
func (f *fakeStore) Rename(_ context.Context, uid, id uuid.UUID, label string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.creds {
		if f.creds[i].ID == id && f.creds[i].UserID == uid {
			f.creds[i].Label = label
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeStore) Delete(_ context.Context, uid, id uuid.UUID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.creds {
		if f.creds[i].ID == id && f.creds[i].UserID == uid {
			f.creds = append(f.creds[:i], f.creds[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeStore) SaveCeremony(_ context.Context, c Ceremony) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ceremonies[c.ID] = c
	return nil
}
func (f *fakeStore) TakeCeremony(_ context.Context, id uuid.UUID, purpose string, now time.Time) (Ceremony, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.ceremonies[id]
	if !ok || c.Purpose != purpose || !c.ExpiresAt.After(now) {
		return Ceremony{}, false, nil
	}
	delete(f.ceremonies, id)
	return c, true, nil
}
func (f *fakeStore) PurgeCeremonies(_ context.Context, now time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for id, c := range f.ceremonies {
		if c.ExpiresAt.Before(now) {
			delete(f.ceremonies, id)
			n++
		}
	}
	return n, nil
}

type rig struct {
	svc  *Service
	st   *fakeStore
	uid  uuid.UUID
	auth *passkeytest.SoftAuth
	now  time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st := newFakeStore()
	svc, err := New(Config{RPID: testRP, Origins: []string{testOrigin}}, st)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{svc: svc, st: st, uid: uuid.New(), auth: passkeytest.New(t), now: time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)}
	svc.SetClock(func() time.Time { return r.now })
	return r
}

// register adds the rig's authenticator as a passkey and returns it.
func (r *rig) register(t *testing.T) Credential {
	t.Helper()
	ctx := context.Background()
	id, opts, err := r.svc.BeginRegistration(ctx, r.uid, "me@example.com", "Me")
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.svc.FinishRegistration(ctx, r.uid, id, r.auth.Create(opts, testRP, testOrigin, true), "My phone", "me@example.com", "Me")
	if err != nil {
		t.Fatalf("registration: %v", err)
	}
	return c
}

func (r *rig) beginLogin(t *testing.T, hydra string) (uuid.UUID, []byte, func(uv bool, origin string, count uint32) []byte) {
	t.Helper()
	id, opts, err := r.svc.BeginLogin(context.Background(), hydra)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := r.st.UserHandle(context.Background(), r.uid)
	return id, h, func(uv bool, origin string, count uint32) []byte {
		return r.auth.Sign(opts, testRP, origin, uv, count, h)
	}
}

func wantRefused(t *testing.T, err error, reason string) {
	t.Helper()
	var ref Refused
	if !errors.As(err, &ref) || ref.Reason != reason {
		t.Errorf("err = %v, want refused (%s)", err, reason)
	}
}

func TestRegisterThenSignIn(t *testing.T) {
	r := newRig(t)
	c := r.register(t)
	if c.Label != "My phone" || !c.BackupEligible || c.UserID != r.uid || len(c.PublicKey) == 0 {
		t.Fatalf("stored passkey %+v", c)
	}
	id, _, sign := r.beginLogin(t, "hydra-1")
	uid, used, err := r.svc.FinishLogin(context.Background(), id, "hydra-1", sign(true, testOrigin, 1))
	if err != nil || uid != r.uid || used.ID != c.ID {
		t.Fatalf("sign-in = %v, %v, %v", uid, used.ID, err)
	}
	if got, _, _ := r.st.CredentialByID(context.Background(), c.CredentialID); got.LastUsedAt == nil || got.SignCount != 1 {
		t.Errorf("use not recorded: %+v", got)
	}
}

// DA: the server's origin check is the boundary. An assertion that carries another subdomain's origin is refused on prod,
// for registration and for sign-in.
func TestOtherOriginsAreRefused(t *testing.T) {
	for _, bad := range []string{"https://preview.rivolt.dev", "https://demo.rivolt.dev", "https://rivolt.dev.evil.example", "http://rivolt.dev", "https://rivolt.dev:8443"} {
		r := newRig(t)
		ctx := context.Background()
		id, opts, _ := r.svc.BeginRegistration(ctx, r.uid, "me@example.com", "Me")
		_, err := r.svc.FinishRegistration(ctx, r.uid, id, r.auth.Create(opts, testRP, bad, true), "x", "me@example.com", "Me")
		wantRefused(t, err, ReasonRegistration)
		r.register(t)
		lid, _, sign := r.beginLogin(t, "h")
		_, _, err = r.svc.FinishLogin(ctx, lid, "h", sign(true, bad, 1))
		wantRefused(t, err, ReasonAssertion)
	}
}

func TestRegistrationWithTheWrongRPIsRefused(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id, opts, _ := r.svc.BeginRegistration(ctx, r.uid, "me@example.com", "Me")
	_, err := r.svc.FinishRegistration(ctx, r.uid, id, r.auth.Create(opts, "preview.rivolt.dev", testOrigin, true), "x", "me@example.com", "Me")
	wantRefused(t, err, ReasonRegistration)
}

// User verification must be set in the authenticator data on both ceremonies: not only requested.
func TestUserVerificationIsRequired(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id, opts, _ := r.svc.BeginRegistration(ctx, r.uid, "me@example.com", "Me")
	_, err := r.svc.FinishRegistration(ctx, r.uid, id, r.auth.Create(opts, testRP, testOrigin, false), "x", "me@example.com", "Me")
	wantRefused(t, err, ReasonNoUV)
	if cs, _ := r.st.Credentials(ctx, r.uid); len(cs) != 0 {
		t.Fatal("a passkey without user verification was stored")
	}
	r.register(t)
	lid, _, sign := r.beginLogin(t, "h")
	_, _, err = r.svc.FinishLogin(ctx, lid, "h", sign(false, testOrigin, 1))
	wantRefused(t, err, ReasonNoUV)
}

func TestCeremoniesAreSingleUseBoundAndExpire(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.register(t)
	// Single use: the same assertion cannot be replayed.
	lid, _, sign := r.beginLogin(t, "h1")
	body := sign(true, testOrigin, 1)
	if _, _, err := r.svc.FinishLogin(ctx, lid, "h1", body); err != nil {
		t.Fatal(err)
	}
	_, _, err := r.svc.FinishLogin(ctx, lid, "h1", body)
	wantRefused(t, err, ReasonCeremony)
	// Bound to its Hydra challenge.
	lid, _, sign = r.beginLogin(t, "h2")
	_, _, err = r.svc.FinishLogin(ctx, lid, "another-challenge", sign(true, testOrigin, 2))
	wantRefused(t, err, ReasonCeremony)
	// Expires after five minutes.
	lid, _, sign = r.beginLogin(t, "h3")
	r.now = r.now.Add(CeremonyTTL + time.Second)
	_, _, err = r.svc.FinishLogin(ctx, lid, "h3", sign(true, testOrigin, 2))
	wantRefused(t, err, ReasonCeremony)
	// A registration ceremony cannot finish a sign-in, nor another account's registration.
	rid, _, _ := r.svc.BeginRegistration(ctx, r.uid, "me@example.com", "Me")
	_, _, err = r.svc.FinishLogin(ctx, rid, "h", []byte("{}"))
	wantRefused(t, err, ReasonCeremony)
	rid2, opts2, _ := r.svc.BeginRegistration(ctx, r.uid, "me@example.com", "Me")
	_, err = r.svc.FinishRegistration(ctx, uuid.New(), rid2, passkeytest.New(t).Create(opts2, testRP, testOrigin, true), "x", "e", "n")
	wantRefused(t, err, ReasonCeremony)
}

func TestUnknownCredentialAndWrongHandleAreRefused(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.register(t)
	// A credential the server does not hold.
	other := passkeytest.New(t)
	lid, opts, _ := r.svc.BeginLogin(ctx, "h")
	h, _ := r.st.UserHandle(ctx, r.uid)
	_, _, err := r.svc.FinishLogin(ctx, lid, "h", other.Sign(opts, testRP, testOrigin, true, 1, h))
	wantRefused(t, err, ReasonUnknown)
	// The right credential with another account's user handle.
	lid, opts, _ = r.svc.BeginLogin(ctx, "h")
	stranger, _ := r.st.UserHandle(ctx, uuid.New())
	_, _, err = r.svc.FinishLogin(ctx, lid, "h", r.auth.Sign(opts, testRP, testOrigin, true, 1, stranger))
	wantRefused(t, err, ReasonUnknown)
	// A signature from another key over the right credential id.
	lid, opts, _ = r.svc.BeginLogin(ctx, "h")
	forged := passkeytest.New(t)
	forged.SetCredentialID(r.auth.CredentialID())
	_, _, err = r.svc.FinishLogin(ctx, lid, "h", forged.Sign(opts, testRP, testOrigin, true, 1, h))
	wantRefused(t, err, ReasonAssertion)
}

func TestASignCounterThatGoesBackwardsIsRefused(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.register(t)
	lid, _, sign := r.beginLogin(t, "a")
	if _, _, err := r.svc.FinishLogin(ctx, lid, "a", sign(true, testOrigin, 10)); err != nil {
		t.Fatal(err)
	}
	lid, _, sign = r.beginLogin(t, "b")
	_, _, err := r.svc.FinishLogin(ctx, lid, "b", sign(true, testOrigin, 9))
	wantRefused(t, err, ReasonSignCount)
	// An authenticator that keeps no counter (always 0) is fine.
	r2 := newRig(t)
	r2.register(t)
	for i := 0; i < 3; i++ {
		lid, _, sign = r2.beginLogin(t, "z")
		if _, _, err := r2.svc.FinishLogin(ctx, lid, "z", sign(true, testOrigin, 0)); err != nil {
			t.Fatalf("a counter that stays 0 should be accepted (synced passkeys): %v", err)
		}
	}
}

func TestPasskeyLimitsAndExclusion(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.register(t)
	// The same authenticator is excluded from a second registration.
	_, opts, err := r.svc.BeginRegistration(ctx, r.uid, "me@example.com", "Me")
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.Response.CredentialExcludeList) != 1 {
		t.Errorf("exclude list has %d entries, want 1", len(opts.Response.CredentialExcludeList))
	}
	// Registration asks for a resident key and user verification, with no attestation.
	sel := opts.Response.AuthenticatorSelection
	if sel.ResidentKey != "required" || sel.UserVerification != "required" || opts.Response.Attestation != "none" {
		t.Errorf("selection %+v attestation %q", sel, opts.Response.Attestation)
	}
	for i := 0; i < MaxPerAccount; i++ {
		_ = r.st.Add(ctx, Credential{ID: uuid.New(), UserID: r.uid, CredentialID: []byte{byte(i), 1, 2}, PublicKey: []byte{1}, Label: "x"})
	}
	if _, _, err := r.svc.BeginRegistration(ctx, r.uid, "me@example.com", "Me"); !errors.Is(err, ErrTooMany) {
		t.Errorf("err = %v, want ErrTooMany", err)
	}
}

func TestConfig(t *testing.T) {
	if (Config{}).Enabled() || (Config{RPID: "rivolt.dev"}).Enabled() {
		t.Error("a config without an RP ID and origins is enabled")
	}
	got := ConfigFromEnv(func(k string) string {
		return map[string]string{"RIVOLT_WEBAUTHN_RP_ID": " rivolt.dev ", "RIVOLT_WEBAUTHN_ORIGINS": "https://rivolt.dev, ,https://x.dev"}[k]
	})
	if !got.Enabled() || got.RPID != "rivolt.dev" || len(got.Origins) != 2 {
		t.Errorf("config %+v", got)
	}
	if _, err := New(Config{}, newFakeStore()); err == nil {
		t.Error("New accepted an empty config")
	}
}

// DA: attestation conveyance is none and no FIDO metadata provider is set, so the library makes no outbound call.
func TestNoMetadataServiceAndNoAttestation(t *testing.T) {
	r := newRig(t)
	if r.svc.wa.Config.MDS != nil {
		t.Error("a FIDO metadata provider is configured")
	}
	if r.svc.wa.Config.AttestationPreference != "none" {
		t.Errorf("attestation preference = %q", r.svc.wa.Config.AttestationPreference)
	}
}
