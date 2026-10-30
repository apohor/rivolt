package charges

import (
	"context"
	"errors"
	"testing"

	"github.com/apohor/rivolt/internal/fence"
)

// A query without the fence placeholder is refused before it touches the database, with or without a
// token in the context: a caller that forgets it cannot run an owner write unguarded.
func TestExecOwnerWriteRefusesAQueryWithoutThePlaceholder(t *testing.T) {
	s := &Store{} // no database: the refusal must come first
	for _, ctx := range []context.Context{
		context.Background(),
		fence.With(context.Background(), fence.Token{Group: "g", Pod: "p", Epoch: 1}),
	} {
		if n, err := s.execOwnerWrite(ctx, `UPDATE charges SET final_state = 'x' WHERE user_id = $1`, 1); !errors.Is(err, errMissingLease) || n != 0 {
			t.Errorf("err = %v n = %d, want errMissingLease and 0", err, n)
		}
	}
}
