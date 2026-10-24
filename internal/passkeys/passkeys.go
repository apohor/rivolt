// Package passkeys is Rivolt's WebAuthn: passkeys as a second way to sign in to an email account, next to the password
// (Sprint 10; DA-approved design). The server runs the ceremonies (github.com/go-webauthn/webauthn) and keeps only public
// values: a credential id, a public key, a counter and a few flags. Kratos is not involved: a good assertion resolves to a
// Rivolt user, and the caller accepts the Hydra login exactly as the password path does.
//
// Rules, from DA: the relying party is the configured RP ID with its origins matched exactly; attestation is "none" (no
// FIDO metadata service, no outbound call from the library); user verification is required on both ceremonies and checked in
// the authenticator data, not only requested; discoverable (resident) credentials are required so sign-in needs no username;
// a ceremony is single use and expires in five minutes; a signature counter that goes backwards is refused. Nothing it logs
// carries a credential id, public key, challenge or user handle.
package passkeys

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/google/uuid"
)

// CeremonyTTL is how long a registration or sign-in may take.
const CeremonyTTL = 5 * time.Minute

// MaxPerAccount is the most passkeys one account may hold.
const MaxPerAccount = 10

// Config is the relying party. Disabled (the zero value) unless RPID and Origins are set.
type Config struct {
	RPID    string   // e.g. "rivolt.dev"
	RPName  string   // shown by the authenticator; default "Rivolt"
	Origins []string // exact origins, e.g. "https://rivolt.dev"
}

// Enabled reports whether passkeys are configured.
func (c Config) Enabled() bool { return strings.TrimSpace(c.RPID) != "" && len(c.Origins) > 0 }

// ConfigFromEnv reads RIVOLT_WEBAUTHN_RP_ID, RIVOLT_WEBAUTHN_RP_NAME and RIVOLT_WEBAUTHN_ORIGINS (comma separated).
func ConfigFromEnv(get func(string) string) Config {
	c := Config{RPID: strings.TrimSpace(get("RIVOLT_WEBAUTHN_RP_ID")), RPName: strings.TrimSpace(get("RIVOLT_WEBAUTHN_RP_NAME"))}
	for _, o := range strings.Split(get("RIVOLT_WEBAUTHN_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.Origins = append(c.Origins, o)
		}
	}
	return c
}

// Credential is one passkey as the server keeps it.
type Credential struct {
	ID             uuid.UUID
	UserID         uuid.UUID
	CredentialID   []byte
	PublicKey      []byte
	SignCount      uint32
	Transports     []string
	AAGUID         []byte
	BackupEligible bool
	BackupState    bool
	Label          string
	CreatedAt      time.Time
	LastUsedAt     *time.Time
}

// Ceremony is the stored state of a registration or sign-in in progress.
type Ceremony struct {
	ID             uuid.UUID
	Purpose        string // "register" or "login"
	UserID         uuid.UUID
	HydraChallenge string
	Session        []byte // webauthn.SessionData as JSON
	ExpiresAt      time.Time
}

// Store is the passkeys' persistence (a Postgres store, or a fake in tests).
type Store interface {
	// UserHandle returns the account's WebAuthn user handle, creating a random one the first time.
	UserHandle(ctx context.Context, uid uuid.UUID) ([]byte, error)
	// UserByHandle finds the account a handle belongs to.
	UserByHandle(ctx context.Context, handle []byte) (uuid.UUID, bool, error)
	Credentials(ctx context.Context, uid uuid.UUID) ([]Credential, error)
	// CredentialByID finds a credential by the authenticator's credential id.
	CredentialByID(ctx context.Context, credentialID []byte) (Credential, bool, error)
	Add(ctx context.Context, c Credential) error
	// RecordUse stores the new signature counter and backup state and the time of use.
	RecordUse(ctx context.Context, credentialID []byte, signCount uint32, backupState bool, at time.Time) error
	Rename(ctx context.Context, uid, id uuid.UUID, label string) (bool, error)
	Delete(ctx context.Context, uid, id uuid.UUID) (bool, error)
	SaveCeremony(ctx context.Context, c Ceremony) error
	// TakeCeremony returns an unexpired ceremony of the purpose and deletes it, so it can be used once.
	TakeCeremony(ctx context.Context, id uuid.UUID, purpose string, now time.Time) (Ceremony, bool, error)
	PurgeCeremonies(ctx context.Context, now time.Time) (int64, error)
}

// Refusals. Each carries a fixed reason, safe as a metric label; to the person they all read alike.
type Refused struct{ Reason string }

func (r Refused) Error() string { return "passkeys: refused (" + r.Reason + ")" }

// Reasons.
const (
	ReasonCeremony     = "ceremony" // unknown, expired, used or someone else's
	ReasonUnknown      = "unknown_credential"
	ReasonAssertion    = "assertion" // the signature, origin, RP or challenge did not check out
	ReasonNoUV         = "no_user_verification"
	ReasonSignCount    = "sign_count"
	ReasonRegistration = "registration"
)

// Service runs the ceremonies.
type Service struct {
	wa  *webauthn.WebAuthn
	st  Store
	now func() time.Time
}

// New builds the service for cfg.
func New(cfg Config, st Store) (*Service, error) {
	if !cfg.Enabled() {
		return nil, errors.New("passkeys: not configured")
	}
	name := cfg.RPName
	if name == "" {
		name = "Rivolt"
	}
	rrk := true
	wa, err := webauthn.New(&webauthn.Config{
		RPID: cfg.RPID, RPDisplayName: name, RPOrigins: cfg.Origins,
		AttestationPreference: protocol.PreferNoAttestation,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: &rrk,
			UserVerification: protocol.VerificationRequired,
		},
		// No MDS: no metadata fetch, no outbound call from the library.
	})
	if err != nil {
		return nil, fmt.Errorf("passkeys: %w", err)
	}
	return &Service{wa: wa, st: st, now: time.Now}, nil
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Store returns the persistence (for the list, rename and delete routes).
func (s *Service) Store() Store { return s.st }

type waUser struct {
	handle        []byte
	name, display string
	creds         []webauthn.Credential
}

func (u waUser) WebAuthnID() []byte                         { return u.handle }
func (u waUser) WebAuthnName() string                       { return u.name }
func (u waUser) WebAuthnDisplayName() string                { return u.display }
func (u waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func toWebAuthn(c Credential) webauthn.Credential {
	var tr []protocol.AuthenticatorTransport
	for _, t := range c.Transports {
		tr = append(tr, protocol.AuthenticatorTransport(t))
	}
	return webauthn.Credential{
		ID: c.CredentialID, PublicKey: c.PublicKey, Transport: tr,
		Flags:         webauthn.CredentialFlags{UserPresent: true, UserVerified: true, BackupEligible: c.BackupEligible, BackupState: c.BackupState},
		Authenticator: webauthn.Authenticator{AAGUID: c.AAGUID, SignCount: c.SignCount},
	}
}

func (s *Service) user(ctx context.Context, uid uuid.UUID, name, display string) (waUser, error) {
	handle, err := s.st.UserHandle(ctx, uid)
	if err != nil {
		return waUser{}, err
	}
	creds, err := s.st.Credentials(ctx, uid)
	if err != nil {
		return waUser{}, err
	}
	u := waUser{handle: handle, name: name, display: display}
	for _, c := range creds {
		u.creds = append(u.creds, toWebAuthn(c))
	}
	return u, nil
}

func (s *Service) save(ctx context.Context, purpose string, uid uuid.UUID, hydraChallenge string, sd *webauthn.SessionData) (uuid.UUID, error) {
	raw, err := json.Marshal(sd)
	if err != nil {
		return uuid.Nil, err
	}
	c := Ceremony{ID: uuid.New(), Purpose: purpose, UserID: uid, HydraChallenge: hydraChallenge, Session: raw, ExpiresAt: s.now().Add(CeremonyTTL)}
	if err := s.st.SaveCeremony(ctx, c); err != nil {
		return uuid.Nil, err
	}
	return c.ID, nil
}

// ErrTooMany is a registration refused because the account already holds MaxPerAccount passkeys.
var ErrTooMany = errors.New("passkeys: too many passkeys on this account")

// BeginRegistration starts adding a passkey to the account uid; name is the login (email) shown by the authenticator.
func (s *Service) BeginRegistration(ctx context.Context, uid uuid.UUID, name, display string) (uuid.UUID, *protocol.CredentialCreation, error) {
	u, err := s.user(ctx, uid, name, display)
	if err != nil {
		return uuid.Nil, nil, err
	}
	if len(u.creds) >= MaxPerAccount {
		return uuid.Nil, nil, ErrTooMany
	}
	// The passkeys already on the account are excluded, so one authenticator is not registered twice.
	var exclude []protocol.CredentialDescriptor
	for _, c := range u.creds {
		exclude = append(exclude, c.Descriptor())
	}
	creation, sd, err := s.wa.BeginRegistration(u, webauthn.WithExclusions(exclude), webauthn.WithConveyancePreference(protocol.PreferNoAttestation))
	if err != nil {
		return uuid.Nil, nil, err
	}
	id, err := s.save(ctx, "register", uid, "", sd)
	return id, creation, err
}

// FinishRegistration completes it: body is the browser's PublicKeyCredential JSON. Returns the stored passkey.
func (s *Service) FinishRegistration(ctx context.Context, uid uuid.UUID, ceremony uuid.UUID, body []byte, label, name, display string) (Credential, error) {
	cer, ok, err := s.st.TakeCeremony(ctx, ceremony, "register", s.now())
	if err != nil {
		return Credential{}, err
	}
	if !ok || cer.UserID != uid {
		return Credential{}, Refused{ReasonCeremony}
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(cer.Session, &sd); err != nil {
		return Credential{}, Refused{ReasonCeremony}
	}
	u, err := s.user(ctx, uid, name, display)
	if err != nil {
		return Credential{}, err
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(body)
	if err != nil {
		return Credential{}, Refused{ReasonRegistration}
	}
	// User verification is required and read from the authenticator data, not only requested.
	if !parsed.Response.AttestationObject.AuthData.Flags.HasUserVerified() {
		return Credential{}, Refused{ReasonNoUV}
	}
	wc, err := s.wa.CreateCredential(u, sd, parsed)
	if err != nil {
		return Credential{}, Refused{ReasonRegistration}
	}
	label = strings.TrimSpace(label)
	if label == "" || len([]rune(label)) > 60 {
		label = "Passkey"
	}
	var tr []string
	for _, t := range wc.Transport {
		tr = append(tr, string(t))
	}
	c := Credential{
		ID: uuid.New(), UserID: uid, CredentialID: wc.ID, PublicKey: wc.PublicKey, SignCount: wc.Authenticator.SignCount,
		Transports: tr, AAGUID: wc.Authenticator.AAGUID, BackupEligible: wc.Flags.BackupEligible, BackupState: wc.Flags.BackupState,
		Label: label, CreatedAt: s.now(),
	}
	if err := s.st.Add(ctx, c); err != nil {
		return Credential{}, err
	}
	return c, nil
}

// BeginLogin starts a usernameless sign-in bound to a Hydra login challenge.
func (s *Service) BeginLogin(ctx context.Context, hydraChallenge string) (uuid.UUID, *protocol.CredentialAssertion, error) {
	assertion, sd, err := s.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return uuid.Nil, nil, err
	}
	id, err := s.save(ctx, "login", uuid.Nil, hydraChallenge, sd)
	return id, assertion, err
}

// FinishLogin verifies the assertion in body for the ceremony and returns the account and the passkey used. Every failure is
// a Refused with a fixed reason; the caller answers them all alike.
func (s *Service) FinishLogin(ctx context.Context, ceremony uuid.UUID, hydraChallenge string, body []byte) (uuid.UUID, Credential, error) {
	cer, ok, err := s.st.TakeCeremony(ctx, ceremony, "login", s.now())
	if err != nil {
		return uuid.Nil, Credential{}, err
	}
	if !ok || cer.HydraChallenge != hydraChallenge {
		return uuid.Nil, Credential{}, Refused{ReasonCeremony}
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(cer.Session, &sd); err != nil {
		return uuid.Nil, Credential{}, Refused{ReasonCeremony}
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(body)
	if err != nil {
		return uuid.Nil, Credential{}, Refused{ReasonAssertion}
	}
	if !parsed.Response.AuthenticatorData.Flags.HasUserVerified() {
		return uuid.Nil, Credential{}, Refused{ReasonNoUV}
	}
	var stored Credential
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		c, found, err := s.st.CredentialByID(ctx, rawID)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, errUnknown
		}
		uid, ok, err := s.st.UserByHandle(ctx, userHandle)
		if err != nil {
			return nil, err
		}
		if !ok || uid != c.UserID {
			return nil, errUnknown // the handle the authenticator holds must be this credential's account
		}
		stored = c
		return s.user(ctx, c.UserID, "", "")
	}
	_, wc, err := s.wa.ValidatePasskeyLogin(handler, sd, parsed)
	if err != nil {
		if errors.Is(err, errUnknown) || strings.Contains(err.Error(), errUnknown.Error()) {
			return uuid.Nil, Credential{}, Refused{ReasonUnknown}
		}
		return uuid.Nil, Credential{}, Refused{ReasonAssertion}
	}
	// A counter that did not go up (when the authenticator keeps one) means a copied key or a malfunction: refuse.
	if wc.Authenticator.CloneWarning {
		return uuid.Nil, Credential{}, Refused{ReasonSignCount}
	}
	at := s.now()
	if err := s.st.RecordUse(ctx, stored.CredentialID, wc.Authenticator.SignCount, wc.Flags.BackupState, at); err != nil {
		return uuid.Nil, Credential{}, err
	}
	stored.SignCount, stored.BackupState, stored.LastUsedAt = wc.Authenticator.SignCount, wc.Flags.BackupState, &at
	return stored.UserID, stored, nil
}

var errUnknown = errors.New("passkeys: unknown credential")

// newHandle is a fresh random user handle.
func NewHandle() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}
