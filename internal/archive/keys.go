package archive

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/crypto"
)

// A user's archive data key is random, sealed with the app's KEK (the same
// envelope as Rivian sessions, bound to the user id) and stored in
// archive_keys. Deleting the row, which account deletion does by cascade,
// makes every archive object of that user unreadable wherever a copy is.

// ErrNoKEK refuses archiving without a real KEK: the no-op sealer would store
// data keys in the clear.
var ErrNoKEK = errors.New("archive: needs RIVOLT_KEK (data keys can't be stored unsealed)")

// NewDataKey returns a fresh random data key.
func NewDataKey() ([]byte, error) {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		return nil, err
	}
	return k, nil
}

// SealDataKey seals key for uid with the KEK.
func SealDataKey(ctx context.Context, s crypto.Sealer, uid uuid.UUID, key []byte) ([]byte, error) {
	if err := realSealer(s); err != nil {
		return nil, err
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("archive: key is %d bytes, want %d", len(key), KeySize)
	}
	return s.Seal(ctx, uid, key)
}

// OpenDataKey opens a sealed data key for uid.
func OpenDataKey(ctx context.Context, s crypto.Sealer, uid uuid.UUID, sealed []byte) ([]byte, error) {
	if err := realSealer(s); err != nil {
		return nil, err
	}
	k, err := s.Open(ctx, uid, sealed)
	if err != nil {
		return nil, fmt.Errorf("archive: open data key: %w", err)
	}
	if len(k) != KeySize {
		return nil, fmt.Errorf("archive: data key is %d bytes, want %d", len(k), KeySize)
	}
	return k, nil
}

func realSealer(s crypto.Sealer) error {
	switch s.(type) {
	case nil, crypto.NoopSealer, *crypto.NoopSealer:
		return ErrNoKEK
	}
	return nil
}

// ObjectAAD is the associated data binding an object to its place: the
// archive format version, the user, the vehicle and the month. An object
// moved to another user's or vehicle's path fails to open.
func ObjectAAD(uid, vehicleID uuid.UUID, month string) []byte {
	return []byte("rivolt-archive/v1|" + uid.String() + "|" + vehicleID.String() + "|" + month)
}
