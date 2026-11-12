package receipts

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// SendEvery is the shortest gap between two on-request syncs sent for one user. It matches the syncer's 30-minute claim interval.
const SendEvery = 30 * time.Minute

// SendOnce is the per-user send window for the first-screen receipts_sync sender, in Valkey (SET NX EX under the app's key prefix). It lives
// outside the receipts tables on purpose: opting out purges rivian_receipt_sync, and with it the Postgres claim, so a toggle loop would
// otherwise get a fresh Rivian call each time. Without Valkey, or when it errors, nothing is sent: the owner's sweep is the fallback, and a
// toggle loop must not reach Rivian.
func SendOnce(rdb *redis.Client, prefix string) func(context.Context, uuid.UUID) bool {
	return func(ctx context.Context, uid uuid.UUID) bool {
		if rdb == nil {
			return false
		}
		ok, err := rdb.SetNX(ctx, prefix+":receipts_sync:"+uid.String(), "1", SendEvery).Result()
		return err == nil && ok
	}
}
