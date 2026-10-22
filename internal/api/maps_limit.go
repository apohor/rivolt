package api

import (
	"net/http"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/maps"
)

// Per-user ceilings for the same-origin map proxies. Valhalla calls are
// real compute (one per trip render); PMTiles is many small range reads
// per pan, so its ceiling is far higher.
const (
	mapsValhallaPerMin = 60
	mapsTilesPerMin    = 1200
	// Each call walks a tile box on the server (bounded by the corridor caps),
	// so a re-render per route edit is fine and a loop is not.
	mapsChargersAlongPerMin = 30
)

// mapsUserLimiter buckets by the signed-in user, so one account cannot
// drain the shared Valhalla/tile servers from many IPs.
func mapsUserLimiter(perMin int) *ipLimiter {
	l := newIPLimiter(perMin, time.Minute, time.Hour)
	l.key = func(r *http.Request) string {
		if uid, ok := auth.UserFromContext(r.Context()); ok {
			return uid.String()
		}
		return clientIP(r)
	}
	return l
}

// valhallaMount and tilesMount are what api.go mounts at /api/maps/valhalla and
// /api/maps/tiles. The prefix is stripped first, so the allowlist sees the path
// the upstream will see ("/route", "/us.pmtiles"), not the public one.
func valhallaMount(proxy http.Handler) http.Handler {
	return mapsUserLimiter(mapsValhallaPerMin).Middleware(
		http.StripPrefix("/api/maps/valhalla", maps.AllowValhalla(maps.LimitValhalla(proxy))))
}

func tilesMount(proxy http.Handler) http.Handler {
	return mapsUserLimiter(mapsTilesPerMin).Middleware(
		http.StripPrefix("/api/maps/tiles", maps.AllowTiles(proxy)))
}
