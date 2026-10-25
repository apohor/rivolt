package samples

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/archive"
	"github.com/apohor/rivolt/internal/auth"
)

type neverReadArchive struct{ called bool }

func (n *neverReadArchive) Objects(context.Context, uuid.UUID) ([]archive.ObjectRef, error) {
	n.called = true
	return nil, nil
}

func (n *neverReadArchive) EachRow(context.Context, uuid.UUID, uuid.UUID, time.Time, func(archive.Row) error) error {
	n.called = true
	return nil
}

// A backup whose context carries no user, or another user, fails before it touches the archive: read through the request pool it
// would find no objects and come back as a complete-looking backup missing its thinned months.
func TestEachAllArchivedRequiresTheUsersContext(t *testing.T) {
	uid := uuid.New()
	s := &Store{userID: uid} // no database: the guard must fire before any query
	for name, ctx := range map[string]context.Context{
		"no user":      context.Background(),
		"another user": auth.WithUser(context.Background(), uuid.New()),
	} {
		src := &neverReadArchive{}
		_, err := s.EachAllArchived(ctx, src, func(Sample) error { return nil })
		if !errors.Is(err, ErrBackupUnavailable) || src.called {
			t.Errorf("%s: err = %v, archive read = %v; want ErrBackupUnavailable and no read", name, err, src.called)
		}
	}
}
