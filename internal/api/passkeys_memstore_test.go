package api

import (
	"bytes"
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/passkeys"
)

// memPasskeyStore is an in-memory passkeys.Store for the router tests.
type memPasskeyStore struct {
	mu         sync.Mutex
	handles    map[uuid.UUID][]byte
	creds      []passkeys.Credential
	ceremonies map[uuid.UUID]passkeys.Ceremony
}

func newMemPasskeyStore() *memPasskeyStore {
	return &memPasskeyStore{handles: map[uuid.UUID][]byte{}, ceremonies: map[uuid.UUID]passkeys.Ceremony{}}
}

func (f *memPasskeyStore) UserHandle(_ context.Context, uid uuid.UUID) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h, ok := f.handles[uid]; ok {
		return h, nil
	}
	f.handles[uid] = passkeys.NewHandle()
	return f.handles[uid], nil
}
func (f *memPasskeyStore) UserByHandle(_ context.Context, handle []byte) (uuid.UUID, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for u, h := range f.handles {
		if bytes.Equal(h, handle) {
			return u, true, nil
		}
	}
	return uuid.Nil, false, nil
}
func (f *memPasskeyStore) Credentials(_ context.Context, uid uuid.UUID) ([]passkeys.Credential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []passkeys.Credential
	for _, c := range f.creds {
		if c.UserID == uid {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *memPasskeyStore) CredentialByID(_ context.Context, id []byte) (passkeys.Credential, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.creds {
		if bytes.Equal(c.CredentialID, id) {
			return c, true, nil
		}
	}
	return passkeys.Credential{}, false, nil
}
func (f *memPasskeyStore) Add(_ context.Context, c passkeys.Credential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creds = append(f.creds, c)
	return nil
}
func (f *memPasskeyStore) RecordUse(_ context.Context, id []byte, n uint32, bs bool, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.creds {
		if bytes.Equal(f.creds[i].CredentialID, id) {
			f.creds[i].SignCount, f.creds[i].BackupState, f.creds[i].LastUsedAt = n, bs, &at
		}
	}
	return nil
}
func (f *memPasskeyStore) Rename(_ context.Context, uid, id uuid.UUID, label string) (bool, error) {
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
func (f *memPasskeyStore) Delete(_ context.Context, uid, id uuid.UUID) (bool, error) {
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
func (f *memPasskeyStore) SaveCeremony(_ context.Context, c passkeys.Ceremony) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ceremonies[c.ID] = c
	return nil
}
func (f *memPasskeyStore) TakeCeremony(_ context.Context, id uuid.UUID, purpose string, now time.Time) (passkeys.Ceremony, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.ceremonies[id]
	if !ok || c.Purpose != purpose || !c.ExpiresAt.After(now) {
		return passkeys.Ceremony{}, false, nil
	}
	delete(f.ceremonies, id)
	return c, true, nil
}
func (f *memPasskeyStore) PurgeCeremonies(_ context.Context, now time.Time) (int64, error) {
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
