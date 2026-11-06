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
		for _, k := range want {
			if h.POI.matches(filters[k]) {
				c.Kinds = append(c.Kinds, k)
			}
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
