// Package analytics hosts local, deterministic analyses over the user's
// driving and charging history. Nothing in this package calls an LLM or
// leaves the backend — these are pure-Go heuristics used to power UI
// affordances (Home vs. public charging badges, anomaly flags, etc.).
//
// The first feature lives here: DBSCAN clustering over charge
// locations. Charges sharing a ~200m neighbourhood get grouped together,
// and the largest cluster is labelled "Home". The second-largest, if it
// looks stable (>= 3 sessions), is labelled "Work". Everything else
// becomes "Public".
//
// DBSCAN is chosen over k-means because:
//   - the number of clusters is unknown (we want to discover it);
//   - public charging sessions are genuine outliers and shouldn't warp
//     a centroid the way k-means would;
//   - ~200m is a natural epsilon for "same parking lot / driveway".
//
// The distance metric is haversine on (lat, lon) so clusters behave
// the same at any latitude. We keep the implementation small and
// dependency-free rather than pulling in a geo library.
package analytics

import (
	"math"
	"sort"
)

// ChargePoint is the minimal projection of a charge session needed to
// cluster it. Store.Charge is denormalised into this shape at the API
// boundary so analytics stays decoupled from the SQL schema.
type ChargePoint struct {
	ID             string
	Lat, Lon       float64
	EnergyAddedKWh float64
}

// ClusterLabel is the human-readable category assigned to a cluster
// after DBSCAN finishes. Labels are derived from size only — the
// largest cluster wins "Home", the second-largest (if meaningful) wins
// "Work", the rest are "Public". This is intentional: we never look at
// timestamps or day-of-week because users vary and the UI only needs
// a coarse tag.
type ClusterLabel string

const (
	LabelHome    ClusterLabel = "Home"
	LabelWork    ClusterLabel = "Work"
	LabelPublic  ClusterLabel = "Public"
	LabelUnknown ClusterLabel = "" // used for charges with no GPS fix
)

// Cluster is a group of charges sharing a neighbourhood.
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

// Params controls the clustering. Defaults match the "same parking lot"
// intuition: 200m radius, 2 sessions minimum to count as a cluster.
type Params struct {
	// EpsilonMeters is the neighbourhood radius used by DBSCAN.
	EpsilonMeters float64
	// MinPoints is the minimum cluster size. Points in a neighbourhood
	// smaller than this are marked as noise and dropped.
	MinPoints int
	// WorkMinSessions is the minimum size for the second cluster to
	// earn the "Work" label. Below this it's treated as "Public" so
	// we don't tag a random repeat L3 stop as the user's office.
	WorkMinSessions int
}

// DefaultParams is the tuning the UI uses by default.
func DefaultParams() Params {
	return Params{
		EpsilonMeters:   200,
		MinPoints:       2,
		WorkMinSessions: 3,
	}
}

// Cluster runs DBSCAN over points and returns the resulting clusters
// sorted largest-first, with labels assigned. Points with missing GPS
// (both Lat and Lon zero) are excluded from clustering entirely — they
// come back in a dedicated LabelUnknown cluster so the caller can still
// render them.
func ClusterCharges(points []ChargePoint, p Params) []Cluster {
	if p.EpsilonMeters <= 0 {
		p = DefaultParams()
	}
	if p.MinPoints < 1 {
		p.MinPoints = 1
	}

	var geo []ChargePoint
	var missing []ChargePoint
	for _, pt := range points {
		if pt.Lat == 0 && pt.Lon == 0 {
			missing = append(missing, pt)
			continue
		}
		geo = append(geo, pt)
	}

	// Standard DBSCAN. We carry an explicit visited bitmap so noise
	// points that later get absorbed into a cluster don't get scanned
	// twice. label[i] == 0 means unassigned; >0 is a cluster id;
	// -1 is noise.
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
			labels[i] = -1 // noise for now, may be claimed below
			continue
		}
		cid++
		labels[i] = cid
		expandCluster(geo, labels, visited, neigh, cid, p.EpsilonMeters, p.MinPoints)
	}

	// Group by cluster id. Noise (-1) becomes its own "Public" bucket
	// because even one-off public stops deserve a marker on the map;
	// we only drop them from the cluster summary by not naming them.
	groups := map[int][]int{}
	for i, l := range labels {
		groups[l] = append(groups[l], i)
	}

	clusters := make([]Cluster, 0, len(groups))
	for l, idxs := range groups {
		if l == -1 {
			// Emit each noise point as its own single-session cluster so
			// the UI can still colour them on /charges without lumping
			// unrelated one-offs onto the same pin.
			for _, i := range idxs {
				clusters = append(clusters, singleton(geo[i]))
			}
			continue
		}
		clusters = append(clusters, build(geo, idxs))
	}

	// Largest first.
	sort.SliceStable(clusters, func(i, j int) bool {
		if clusters[i].Sessions != clusters[j].Sessions {
			return clusters[i].Sessions > clusters[j].Sessions
		}
		return clusters[i].EnergyKWh > clusters[j].EnergyKWh
	})

	// Label. Home goes to the biggest multi-session cluster; Work to
	// the next-biggest that meets WorkMinSessions; everything else is
	// Public. Singletons (from noise) are always Public.
	homeAssigned := false
	workAssigned := false
	for i := range clusters {
		c := &clusters[i]
		if c.Sessions >= p.MinPoints && !homeAssigned {
			c.Label = LabelHome
			homeAssigned = true
			continue
		}
		if c.Sessions >= p.WorkMinSessions && !workAssigned {
			c.Label = LabelWork
			workAssigned = true
			continue
		}
		c.Label = LabelPublic
	}

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
