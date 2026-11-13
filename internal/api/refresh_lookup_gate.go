package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/auth"
)

// refreshLookupOpTimeout bounds the Redis calls of the gate: a slow Redis must not hold a "Check again" tap.
const refreshLookupOpTimeout = 300 * time.Millisecond

// refreshLookupWindow mirrors vehiclediscovery.ForceEvery (the owner's shortest time between two lookups of one user); internal/api may not
// import that module, and a shorter window here only means one more nudge the owner then refuses itself.
const refreshLookupWindow = 10 * time.Minute

type refreshLookupGateKey struct{}

// refreshLookupGate remembers, across pods, that a user's account was just looked up. The owner pod keeps its own ForceEvery limit and never
// replies to a nudge, so without it a pod that does not hold the account could only answer 503 to every tap; with it the second tap inside the
// window gets a 429 the client can tell apart from "the owner was asked".
type refreshLookupGate struct {
	rdb    *redis.Client
	prefix string
	key    []byte
}

func newRefreshLookupGate(rdb *redis.Client, prefix string, key []byte) *refreshLookupGate {
	if rdb == nil || len(key) == 0 {
		return nil
	}
	return &refreshLookupGate{rdb: rdb, prefix: prefix, key: key}
}

func (g *refreshLookupGate) redisKey(uid uuid.UUID) string {
	m := hmac.New(sha256.New, g.key)
	m.Write([]byte("refresh-lookup|" + uid.String()))
	return g.prefix + ":reflk:" + hex.EncodeToString(m.Sum(nil))
}

// claim reports whether this tap may ask the owner to look; when it may not, retryAfter is the seconds left in the window. A Redis failure lets
// the tap through: the owner's own limit still holds.
func (g *refreshLookupGate) claim(ctx context.Context, uid uuid.UUID) (ok bool, retryAfter int) {
	if g == nil {
		return true, 0
	}
	cctx, cancel := context.WithTimeout(ctx, refreshLookupOpTimeout)
	defer cancel()
	k := g.redisKey(uid)
	set, err := g.rdb.SetNX(cctx, k, "1", refreshLookupWindow).Result()
	if err != nil || set {
		return true, 0
	}
	left, err := g.rdb.TTL(cctx, k).Result()
	if err != nil || left <= 0 {
		return false, int(refreshLookupWindow / time.Second)
	}
	return false, int((left + time.Second - 1) / time.Second)
}

// release gives the user's slot back, for a nudge that never reached an owner. It runs detached from the request, so it has its own deadline.
func (g *refreshLookupGate) release(uid uuid.UUID) {
	if g == nil {
		return
	}
	ctx, cancel := context.WithTimeout(auth.WithUser(context.Background(), uid), refreshLookupOpTimeout)
	defer cancel()
	_ = g.rdb.Del(ctx, g.redisKey(uid)).Err()
}

type refreshNudgeKey struct{}

func withRefreshLookupGate(g *refreshLookupGate, nudge func(context.Context, uuid.UUID, func())) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), refreshLookupGateKey{}, g)
			if nudge != nil {
				ctx = context.WithValue(ctx, refreshNudgeKey{}, func(c context.Context, uid uuid.UUID) { nudge(c, uid, func() { g.release(uid) }) })
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// refreshNudgeFrom is the nudge the refresh handler calls: the one that hands the gate's slot back when it is lost, else the plain sign-in nudge.
func refreshNudgeFrom(ctx context.Context) func(context.Context, uuid.UUID) {
	if fn, _ := ctx.Value(refreshNudgeKey{}).(func(context.Context, uuid.UUID)); fn != nil {
		return fn
	}
	return signInNudgeFrom(ctx)
}

func refreshLookupGateFrom(ctx context.Context) *refreshLookupGate {
	g, _ := ctx.Value(refreshLookupGateKey{}).(*refreshLookupGate)
	return g
}
