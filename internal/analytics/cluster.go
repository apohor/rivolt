// Package analytics hosts local, deterministic analyses over the user's
// driving and charging history. Nothing in this package calls an LLM or
// leaves the backend — these are pure-Go heuristics used to power UI
// affordances (Home vs. Public vs. Fast charging badges, anomaly flags,
// etc.).
//
// Charging-location classification has two axes:
//
//  1. Session peak power. Anything at or above FastMinKW is treated as
//     DC fast charging (EA, Supercharger, RAN L3, etc.) and goes into
//     a single "Fast" bucket regardless of where it happened. The
//     threshold (default 50 kW) is the de-facto line between AC L2 and
//     DCFC — no production L2 charger exceeds ~22 kW.
//
//  2. Location, for everything *below* the Fast threshold. Those
//     sessions are DBSCAN-clustered on (lat, lon); the single largest
//     cluster becomes "Home" (your driveway / apartment L2), and every
//     other slow session — other L1/L2 stops, random friend's-house
//     plug-ins, singleton noise — is "Public".
//
// DBSCAN is chosen over k-means for the location step because:
//   - the number of clusters is unknown (we want to discover it);
//   - one-off public charging sessions are genuine outliers and
//     shouldn't warp a centroid the way k-means would;
//   - ~200m is a natural epsilon for "same parking lot / driveway".
//
// The distance metric is haversine on (lat, lon) so clusters behave the
// same at any latitude. We keep the implementation small and
// dependency-free rather than pulling in a geo library.
package analytics

import (
	"math"
	"sort"
)

// ChargePoint is the minimal projection of a charge session needed to
// classify it. Store.Charge is denormalised into this shape at the API
// boundary so analytics stays decoupled from the SQL schema.
type ChargePoint struct {
	ID             string
	Lat, Lon       float64
	EnergyAddedKWh float64
	// MaxPowerKW is the peak kW observed during the session. When >=
	// Params.FastMinKW the charge is bucketed as Fast regardless of
	// location. Zero means unknown — those sessions fall through to
	// location clustering.
	MaxPowerKW float64
}

// ClusterLabel is the human-readable category assigned after
// classification. Three outcomes are possible for GPS-tagged charges:
// Home (largest location cluster of slow sessions), Public (everything
// else slow and located), Fast (peak power >= threshold). Charges
// missing a GPS fix land in LabelUnknown.
type ClusterLabel string

const (
	LabelHome    ClusterLabel = "Home"
	LabelPublic  ClusterLabel = "Public"
	LabelFast    ClusterLabel = "Fast"
	LabelUnknown ClusterLabel = "" // used for charges with no GPS fix
)

// Cluster is a group of charges sharing a category. For Home / Public
// it's a geographic cluster; for Fast it's a power-defined bucket
// whose "centroid" is the mean of its members' coordinates (useful for
// a map pin only if the fast stops happen to cluster geographically).
type Cluster struct {
	Label       ClusterLabel
	Centroid    LatLon // mean of member coordinates
	MemberIDs   []string
	Sessions    int
	EnergyKWh   float64
	RadiusMeter float64 // max member distance from centroid
}

// LatLon is a simple coordinate pair.
type LatLon struct {
	Lat, Lon float64
}

// Params controls both steps of classification.
type Params struct {
	// EpsilonMeters is the neighbourhood radius used by DBSCAN.
	EpsilonMeters float64
	// MinPoints is the minimum cluster size for DBSCAN. Points in a
	// neighbourhood smaller than this are marked as noise and show
	// up as singleton Public clusters.
	MinPoints int
	// FastMinKW is the peak-power threshold at which a session is
	// classified as Fast regardless of location. 50 kW cleanly
	// separates the fastest AC L2 hardware (~22 kW) from DCFC.
	FastMinKW float64
}

// DefaultParams is the tuning the UI uses by default.
func DefaultParams() Params {
	return Params{
		EpsilonMeters: 200,
		MinPoints:     2,
		FastMinKW:     50,
	}
}

// ClusterCharges classifies charges into Home / Public / Fast / Unknown.
//
// Flow:
//  1. Partition inputs into Fast (MaxPowerKW >= FastMinKW), GPS-less
//     (Unknown), and the remaining located-slow pool.
//  2. DBSCAN the slow pool with the haversine metric.
//  3. Label the largest slow cluster Home; everything else slow —
//     smaller clusters AND noise singletons — is Public.
//  4. Emit one aggregate Fast cluster (if any fast sessions exist) and
//     one Unknown cluster (if any no-GPS sessions exist).
//
// Returned slice is ordered largest-first by session count, with
// Unknown last.
func ClusterCharges(points []ChargePoint, p Params) []Cluster {
	if p.EpsilonMeters <= 0 {
		p = DefaultParams()
	}
	if p.MinPoints < 1 {
		p.MinPoints = 1
	}
	if p.FastMinKW <= 0 {
		p.FastMinKW = DefaultParams().FastMinKW
	}

	var fast []ChargePoint
	var geo []ChargePoint
	var missing []ChargePoint
	for _, pt := range points {
		// Fast wins first. A DCFC session happening to be in the home
		// driveway would still be Fast (unusual, but explicit).
		if pt.MaxPowerKW >= p.FastMinKW {
			fast = append(fast, pt)
			continue
		}
		if pt.Lat == 0 && pt.Lon == 0 {
			missing = append(missing, pt)
			continue
		}
		geo = append(geo, pt)
	}

	// Standard DBSCAN over the slow-located pool. labels[i]==0 means
	// unassigned, >0 is a cluster id, -1 is noise.
	n := len(geo)
	labels := make([]int, n)
	visited := make([]bool, n)
	cid := 0
	for i := 0; i < n; i++ {
		if visited[i] {
			continue
		}
		visited[i] = true
		neigh := regionQuery(geo, i, p.EpsilonMeters)
		if len(neigh) < p.MinPoints {
			labels[i] = -1
			continue
		}
		cid++
		labels[i] = cid
		expandCluster(geo, labels, visited, neigh, cid, p.EpsilonMeters, p.MinPoints)
	}

	// Group DBSCAN output. Noise points become singleton clusters so
	// they still render on the map but never win the Home label below.
	groups := map[int][]int{}
	for i, l := range labels {
		groups[l] = append(groups[l], i)
	}
	clusters := make([]Cluster, 0, len(groups))
	for l, idxs := range groups {
		if l == -1 {
			for _, i := range idxs {
				clusters = append(clusters, singleton(geo[i]))
			}
			continue
		}
		clusters = append(clusters, build(geo, idxs))
	}

	// Largest slow cluster first. Energy breaks ties so a single big
	// one-off stop can't outrank a recurring home cluster.
	sort.SliceStable(clusters, func(i, j int) bool {
		if clusters[i].Sessions != clusters[j].Sessions {
			return clusters[i].Sessions > clusters[j].Sessions
		}
		return clusters[i].EnergyKWh > clusters[j].EnergyKWh
	})

	// Home = the single biggest multi-session slow cluster. Everything
	// else slow (smaller clusters, noise singletons) is Public.
	homeAssigned := false
	for i := range clusters {
		c := &clusters[i]
		if !homeAssigned && c.Sessions >= p.MinPoints {
			c.Label = LabelHome
			homeAssigned = true
			continue
		}
		c.Label = LabelPublic
	}

	// Fast bucket: one aggregate entry. A Fast cluster can legitimately
	// span a whole region (road trips are geographically diffuse) so
	// its centroid/radius are informational only; the Overview card
	// reads session + energy totals.
	if len(fast) > 0 {
		clusters = append(clusters, buildFast(fast))
	}

	// Re-sort so the final ordering is largest-first across all
	// categories (Home typically wins, but a road-tripping user could
	// have more Fast sessions than Home in a given window).
	sort.SliceStable(clusters, func(i, j int) bool {
		if clusters[i].Sessions != clusters[j].Sessions {
			return clusters[i].Sessions > clusters[j].Sessions
		}
		return clusters[i].EnergyKWh > clusters[j].EnergyKWh
	})

	if len(missing) > 0 {
		u := Cluster{Label: LabelUnknown}
		for _, pt := range missing {
			u.MemberIDs = append(u.MemberIDs, pt.ID)
			u.EnergyKWh += pt.EnergyAddedKWh
			u.Sessions++
		}
		clusters = append(clusters, u)
	}

	return clusters
}

// expandCluster is the inner loop of DBSCAN: walk the transitive
// closure of ε-reachable neighbours and attach them to cluster cid.
func expandCluster(
	geo []ChargePoint,
	labels []int,
	visited []bool,
	seeds []int,
	cid int,
	eps float64,
	minPts int,
) {
	queue := append([]int{}, seeds...)
	for len(queue) > 0 {
		j := queue[0]
		queue = queue[1:]
		if !visited[j] {
			visited[j] = true
			neigh := regionQuery(geo, j, eps)
			if len(neigh) >= minPts {
				queue = append(queue, neigh...)
			}
		}
		if labels[j] <= 0 {
			labels[j] = cid
		}
	}
}

// regionQuery returns indices within eps metres of geo[i]. O(n²) but
// fine for our corpus (hundreds of charges, not millions).
func regionQuery(geo []ChargePoint, i int, eps float64) []int {
	out := []int{i}
	for j := range geo {
		if j == i {
			continue
		}
		if haversineMeters(geo[i].Lat, geo[i].Lon, geo[j].Lat, geo[j].Lon) <= eps {
			out = append(out, j)
		}
	}
	return out
}

func build(geo []ChargePoint, idxs []int) Cluster {
	var sumLat, sumLon, sumKWh float64
	members := make([]string, 0, len(idxs))
	for _, i := range idxs {
		sumLat += geo[i].Lat
		sumLon += geo[i].Lon
		sumKWh += geo[i].EnergyAddedKWh
		members = append(members, geo[i].ID)
	}
	n := float64(len(idxs))
	centroid := LatLon{Lat: sumLat / n, Lon: sumLon / n}
	// Radius = farthest member from centroid. Useful for pin styling
	// and for deciding whether a cluster is actually one location or
	// a strung-out corridor.
	var maxD float64
	for _, i := range idxs {
		d := haversineMeters(centroid.Lat, centroid.Lon, geo[i].Lat, geo[i].Lon)
		if d > maxD {
			maxD = d
		}
	}
	return Cluster{
		Centroid:    centroid,
		MemberIDs:   members,
		Sessions:    len(idxs),
		EnergyKWh:   sumKWh,
		RadiusMeter: maxD,
	}
}

// buildFast aggregates every high-power session into a single Fast
// cluster. Centroid/radius are computed over only the located members
// (fast stops without a GPS fix still count toward session/energy
// totals but shouldn't distort the pin at lat=0,lon=0).
func buildFast(pts []ChargePoint) Cluster {
	var sumLat, sumLon, sumKWh float64
	var located int
	members := make([]string, 0, len(pts))
	for _, pt := range pts {
		members = append(members, pt.ID)
		sumKWh += pt.EnergyAddedKWh
		if pt.Lat != 0 || pt.Lon != 0 {
			sumLat += pt.Lat
			sumLon += pt.Lon
			located++
		}
	}
	var centroid LatLon
	var maxD float64
	if located > 0 {
		centroid = LatLon{Lat: sumLat / float64(located), Lon: sumLon / float64(located)}
		for _, pt := range pts {
			if pt.Lat == 0 && pt.Lon == 0 {
				continue
			}
			d := haversineMeters(centroid.Lat, centroid.Lon, pt.Lat, pt.Lon)
			if d > maxD {
				maxD = d
			}
		}
	}
	return Cluster{
		Label:       LabelFast,
		Centroid:    centroid,
		MemberIDs:   members,
		Sessions:    len(pts),
		EnergyKWh:   sumKWh,
		RadiusMeter: maxD,
	}
}

func singleton(pt ChargePoint) Cluster {
	return Cluster{
		Centroid:    LatLon{Lat: pt.Lat, Lon: pt.Lon},
		MemberIDs:   []string{pt.ID},
		Sessions:    1,
		EnergyKWh:   pt.EnergyAddedKWh,
		RadiusMeter: 0,
	}
}

// haversineMeters returns the great-circle distance in metres.
func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371000.0 // mean Earth radius in metres
	toRad := func(d float64) float64 { return d * math.Pi / 180 }
	dLat := toRad(lat2 - lat1)
	dLon := toRad(lon2 - lon1)
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(toRad(lat1))*math.Cos(toRad(lat2))*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return R * c
}
