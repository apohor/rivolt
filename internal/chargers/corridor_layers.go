package chargers

import (
	"errors"
	"math"
	"sort"
)

// The layers a planner map can show along a route, in one pass. "all" is not a layer: ask for the ones you draw.
var layerKinds = map[string]Filter{"dcfc": FilterDCFC, "l2": FilterL2, "hotels": FilterHotels, "camp": FilterCamp}

// ErrBadKinds is an empty, unknown or repeated-beyond-reason list of layer kinds.
var ErrBadKinds = errors.New("chargers: kinds must be one or more of dcfc, l2, hotels, camp")

const (
	// DefaultLayerLimit and MaxLayerLimit bound one page of a layer query.
	DefaultLayerLimit = 300
	MaxLayerLimit     = 1000
)

// CorridorPOI is a station with where it sits against the route: AlongKm from the route's start to the closest point on it,
// OffKm from the route to the station, and Kinds, the requested layers it belongs to.
type CorridorPOI struct {
	POI
	AlongKm float64  `json:"alongKm"`
	OffKm   float64  `json:"offKm"`
	Kinds   []string `json:"kinds"`
}

// LayerOptions are the corridor options plus paging along the route: only stations with AlongKm past AfterKm, at most Limit.
type LayerOptions struct {
	QueryCorridorOptions
	AfterKm float64
	Limit   int
	// Paged asks for the route-ordered pages (first Limit, then NextAfterKm as AfterKm). Without it (and with no AfterKm), a corridor with more
	// stations than Limit comes back as a set spread along the whole route (spreadAlongRoute), because a client that shows one capped page
	// would otherwise show only the first stretch of a long route.
	Paged bool
	// KindKm, when set, replaces LayerKindKm as each kind's half-width (a CorridorPresets entry); ignored when CorridorKm is set.
	KindKm map[string]float64
	// MinAlongKm drops every station closer than this to the route's start, along the route. It is applied before paging, so Total and the
	// pages agree.
	MinAlongKm float64
	// MinFastKW drops a station from the fast layer when its power is known and below this. Unknown power stays (as MinPowerKW does).
	MinFastKW float64
	// Accept, when set, keeps only stations it returns true for (applied before paging, so Total and the pages agree).
	Accept func(POI) bool
}

// LayerResult is one page. Total counts every station in the corridor for the requested kinds, so a client can say
// "showing 300 of 1,240". NextAfterKm is set when more follow: ask again with it as AfterKm. AsOf is the newest export
// date among the stations found, else the archive file's own date (the build date, not live data); RouteKm is the length of the route as searched.
type LayerResult struct {
	POIs        []CorridorPOI
	Total       int
	NextAfterKm *float64
	AsOf        string
	RouteKm     float64
}

// LayerKindKm is the default half-width of the corridor for each layer kind, used when the request names no corridor_km: a fast charger a
// driver would detour for is within 5 miles of the route, a Level 2 within 3 (nobody drives further for slow charging), a hotel or a
// campground within 10. The query runs once at the widest requested width and each station is then kept only within its own kind's width.
var LayerKindKm = map[string]float64{"dcfc": 8, "l2": 4.8, "hotels": 16, "camp": 16}

// CorridorPresets are the planner settings' "how far from the route" choices, as per-kind half-widths in km (the miles in the setting's labels:
// close 2/1/5, normal 5/3/10, wide 10/5/20 for fast / Level 2 / hotels and campgrounds). Normal is LayerKindKm, so no preset set means no change.
var CorridorPresets = map[string]map[string]float64{
	"close":  {"dcfc": 3.2, "l2": 1.6, "hotels": 8, "camp": 8},
	"normal": LayerKindKm,
	"wide":   {"dcfc": 16, "l2": 8, "hotels": 32, "camp": 32},
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// QueryLayers returns the stations of the requested kinds along the route in route order (nearest the start first), with
// their distance along and off the route. One scan serves every kind. Archive only: no call leaves the server.
func (a *Archive) QueryLayers(path [][2]float64, kinds []string, opts LayerOptions) (LayerResult, error) {
	if len(kinds) == 0 || len(kinds) > len(layerKinds) {
		return LayerResult{}, ErrBadKinds
	}
	var want []string
	filters := map[string]Filter{}
	for _, k := range kinds {
		f, ok := layerKinds[k]
		if !ok {
			return LayerResult{}, ErrBadKinds
		}
		if _, dup := filters[k]; !dup {
			filters[k] = f
			want = append(want, k)
		}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultLayerLimit
	}
	if limit > MaxLayerLimit {
		limit = MaxLayerLimit
	}
	// No corridor_km: every kind has its own width (LayerKindKm), and the scan runs at the widest of the kinds asked for.
	perKind := opts.CorridorKm <= 0
	kindKm := LayerKindKm
	if opts.KindKm != nil {
		kindKm = opts.KindKm
	}
	if perKind {
		for _, k := range want {
			if w := kindKm[k]; w > opts.CorridorKm {
				opts.CorridorKm = w
			}
		}
	}
	hits, proj, err := a.corridorHits(path, func(p POI) bool {
		if opts.Accept != nil && !opts.Accept(p) {
			return false
		}
		for _, k := range want {
			if p.matches(filters[k]) {
				return true
			}
		}
		return false
	}, opts.QueryCorridorOptions)
	if err != nil {
		return LayerResult{}, err
	}
	all := make([]CorridorPOI, 0, len(hits))
	asof := ""
	for _, h := range hits {
		c := CorridorPOI{POI: h.POI, AlongKm: round3(h.alongM / 1000), OffKm: round3(h.offM / 1000)}
		if c.AlongKm < opts.MinAlongKm {
			continue
		}
		for _, k := range want {
			if k == "dcfc" && opts.MinFastKW > 0 && h.POI.MaxPowerKW > 0 && h.POI.MaxPowerKW < opts.MinFastKW {
				continue
			}
			if h.POI.matches(filters[k]) && (!perKind || c.OffKm <= kindKm[k]) {
				c.Kinds = append(c.Kinds, k)
			}
		}
		if len(c.Kinds) == 0 {
			continue // inside the scan's width but outside every kind it matches
		}
		if h.POI.AsOf > asof {
			asof = h.POI.AsOf
		}
		all = append(all, c)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].AlongKm != all[j].AlongKm {
			return all[i].AlongKm < all[j].AlongKm
		}
		if all[i].EntityID != all[j].EntityID {
			return all[i].EntityID < all[j].EntityID
		}
		if all[i].Lat != all[j].Lat {
			return all[i].Lat < all[j].Lat
		}
		return all[i].Lon < all[j].Lon
	})
	// The newest export date a station carries; stations of the plain NREL layer carry none, so fall back to the archive file's own date
	// (its Last-Modified). Empty only when neither is known: the client then says nothing about the age rather than guess.
	if asof == "" {
		if b := a.BuiltAt(); !b.IsZero() {
			asof = b.Format("2006-01-02")
		}
	}
	res := LayerResult{Total: len(all), AsOf: asof, RouteKm: round3(proj.totalLn / 1000)}
	if !opts.Paged && opts.AfterKm <= 0 && len(all) > limit {
		res.POIs = spreadPerKind(all, want, limit, res.RouteKm)
		return res, nil // one capped answer, no cursor: it is not the first page of a longer list
	}
	// Page: past AfterKm, up to limit, and never split stations that share a distance (a page boundary there would lose or
	// repeat them, since the cursor is the distance).
	start := sort.Search(len(all), func(i int) bool { return all[i].AlongKm > opts.AfterKm })
	if opts.AfterKm <= 0 {
		start = 0
	}
	end := start + limit
	if end < len(all) {
		for end < len(all) && all[end].AlongKm == all[end-1].AlongKm {
			end++
		}
	}
	if end > len(all) {
		end = len(all)
	}
	res.POIs = all[start:end]
	if end < len(all) {
		next := all[end-1].AlongKm
		res.NextAfterKm = &next
	}
	return res, nil
}

// spreadBuckets is how many equal stretches of the route a capped answer is spread over (fewer when the cap is smaller).
const spreadBuckets = 40

// spreadAlongRoute picks limit of the stations idx (indexes into all, which is in route order) so the whole route is covered: the route is cut into equal stretches, each
// stretch that has stations keeps one (when the cap allows), and the rest of the cap is shared out in proportion to how many stations a
// stretch has. Inside a stretch the strongest charger wins (highest power, then nearest the route, then earliest along it), so a dense
// city does not use up the cap and the long empty stretches still show something. The result is in route order.
func spreadAlongRoute(all []CorridorPOI, idx []int, limit int, routeKm float64, byPower bool) []int {
	if limit <= 0 {
		return nil
	}
	if len(idx) <= limit {
		return idx
	}
	nb := spreadBuckets
	if limit < nb {
		nb = limit
	}
	width := routeKm / float64(nb)
	buckets := make([][]int, nb)
	for _, i := range idx {
		p := all[i]
		b := 0
		if width > 0 {
			b = int(p.AlongKm / width)
		}
		if b < 0 {
			b = 0
		}
		if b >= nb {
			b = nb - 1
		}
		buckets[b] = append(buckets[b], i)
	}
	var filled []int
	for b, ix := range buckets {
		if len(ix) > 0 {
			filled = append(filled, b)
		}
	}
	quota := make([]int, nb)
	if limit <= len(filled) {
		// Fewer slots than stretches with stations: evenly spaced stretches get one each.
		for k := 0; k < limit; k++ {
			quota[filled[k*len(filled)/limit]] = 1
		}
	} else {
		rem := limit - len(filled)
		extra := 0 // stations beyond the first in each stretch
		for _, b := range filled {
			quota[b] = 1
			extra += len(buckets[b]) - 1
		}
		if extra > 0 {
			type share struct {
				b    int
				frac float64
			}
			var shares []share
			given := 0
			for _, b := range filled {
				room := len(buckets[b]) - 1
				exact := float64(rem) * float64(room) / float64(extra)
				n := int(exact)
				if n > room {
					n = room
				}
				quota[b] += n
				given += n
				shares = append(shares, share{b, exact - float64(n)})
			}
			sort.SliceStable(shares, func(i, j int) bool { return shares[i].frac > shares[j].frac })
			for _, s := range shares {
				if given >= rem {
					break
				}
				if quota[s.b] < len(buckets[s.b]) {
					quota[s.b]++
					given++
				}
			}
			// Rounding can leave a few slots when small stretches hit their room: fill them wherever stations remain, earliest stretch first.
			for _, b := range filled {
				for given < rem && quota[b] < len(buckets[b]) {
					quota[b]++
					given++
				}
			}
		}
	}
	var keep []int
	for b, ix := range buckets {
		if quota[b] == 0 {
			continue
		}
		sort.SliceStable(ix, func(i, j int) bool {
			a, c := all[ix[i]], all[ix[j]]
			// Nearest the route first: a site 18 miles off with 350 kW must not beat one on the highway with 150 kW. Power only breaks ties.
			if a.OffKm != c.OffKm {
				return a.OffKm < c.OffKm
			}
			if byPower && a.MaxPowerKW != c.MaxPowerKW {
				return a.MaxPowerKW > c.MaxPowerKW
			}
			return a.AlongKm < c.AlongKm
		})
		keep = append(keep, ix[:quota[b]]...)
	}
	sort.Ints(keep) // all is in route order, so the indices are
	return keep
}

// spreadPerKind splits the cap evenly over the requested kinds (a kind with fewer stations than its share hands the rest to the others),
// spreads each kind's stations along the route on its own, and merges in route order. Chargers rank by power inside a stretch; hotels and
// campgrounds have no power, so they rank by distance from the route. A station in two kinds is kept once. Without this, chargers outrank
// hotels everywhere and the hotels vanish on exactly the long routes the spread is for.
func spreadPerKind(all []CorridorPOI, kinds []string, limit int, routeKm float64) []CorridorPOI {
	byKind := make([][]int, len(kinds))
	for i, p := range all {
		for k, kind := range kinds {
			for _, pk := range p.Kinds {
				if pk == kind {
					byKind[k] = append(byKind[k], i)
					break
				}
			}
		}
	}
	// Water-filling: equal shares, the unused part of a small kind redistributed. The allocations always add up to at most limit.
	alloc := make([]int, len(kinds))
	done := make([]bool, len(kinds))
	remaining := limit
	for remaining > 0 {
		open := 0
		for k := range kinds {
			if !done[k] {
				open++
			}
		}
		if open == 0 {
			break
		}
		if remaining < open {
			// Fewer slots than kinds still open: one each for the first `remaining` of them, and no more.
			for k := range kinds {
				if remaining == 0 {
					break
				}
				if !done[k] && len(byKind[k]) > 0 {
					alloc[k], done[k] = 1, true
					remaining--
				}
			}
			break
		}
		share := remaining / open
		progressed := false
		for k := range kinds {
			if !done[k] && len(byKind[k]) <= share {
				alloc[k], done[k] = len(byKind[k]), true
				remaining -= len(byKind[k])
				progressed = true
			}
		}
		if progressed {
			continue
		}
		// Every open kind has more than its share: split what is left, the remainder to the first kinds.
		extra := remaining - share*open
		for k := range kinds {
			if !done[k] {
				alloc[k] = share
				if extra > 0 {
					alloc[k]++
					extra--
				}
				done[k] = true
			}
		}
		break
	}
	keep := map[int]bool{}
	for k, kind := range kinds {
		power := kind == "dcfc" || kind == "l2"
		for _, i := range spreadAlongRoute(all, byKind[k], alloc[k], routeKm, power) {
			keep[i] = true
		}
	}
	idx := make([]int, 0, len(keep))
	for i := range keep {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	out := make([]CorridorPOI, 0, len(idx))
	for _, i := range idx {
		out = append(out, all[i])
	}
	return out
}
