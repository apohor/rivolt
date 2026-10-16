package dcfcrates

import (
	"encoding/json"
	"math"
	"sort"
)

// DC fast charge network detection (contract dcfc-auto-network v1):
// which network a recorded DC fast charge Rivian didn't bill was on, how
// sure we are, and why. Pure: callers gather the inputs (Rivian's
// isRivianCharger, the user's own verified charges nearby, archive
// stations nearby) and price the result with ResolveRate.

// Verify states (charges.network_state).
const (
	StateEstimated         = "estimated"
	StateNeedsConfirmation = "needs_confirmation"
	StateConfirmed         = "confirmed"
)

// ConfidentAt is the confidence an estimate needs to stand without
// asking the user (state estimated rather than needs_confirmation).
const ConfidentAt = 0.85

// Detection radii, metres.
const (
	SiteMemoryRadiusM = 100.0 // a verified charge of the user's own at the same site
	NearRadiusM       = 150.0 // one network this close: confident
	SiteRadiusM       = 250.0 // stations this close count as the site
)

// Candidate is an archive DC fast charging station near the charge.
type Candidate struct {
	Name       string  // station name
	Network    string  // the archive's network field (NREL ev_network, renamed)
	Operator   string  // the archive's operator field (often a URL)
	DistM      float64 // from the charge position
	MaxPowerKW float64 // 0 when unknown
}

// SiteMemory is the nearest DC fast charge of the user's own whose
// network they confirmed or set themselves.
type SiteMemory struct {
	Network string // display name as stored on that charge
	DistM   float64
}

// DetectInput is everything Detect looks at.
type DetectInput struct {
	RivianCharger bool        // Rivian reported isRivianCharger for this DC session
	Site          *SiteMemory // nil when none within SiteMemoryRadiusM
	Candidates    []Candidate // DC stations within SiteRadiusM, any order
	PeakKW        float64     // the session's peak power, 0 when unknown
}

// Detection is Detect's verdict. Query is what to price with
// (ResolveRate(Query, user overrides)); empty Query means nothing to
// price: the charge stays unpriced and needs the user.
type Detection struct {
	Query      string
	Network    string // display name of the detected network ("" when none)
	State      string
	Confidence float64
	Evidence   json.RawMessage
}

type evidenceCandidate struct {
	Network string  `json:"network"`
	Slug    string  `json:"slug"`
	DistM   float64 `json:"dist_m"`
}

type evidence struct {
	V          int                 `json:"v"`
	Source     string              `json:"source"`
	Candidates []evidenceCandidate `json:"candidates"`
	NodeCheck  *nodeCheck          `json:"node_check,omitempty"`
	Pos        string              `json:"pos,omitempty"`
}

type nodeCheck struct {
	Key   string `json:"key"`
	Agree bool   `json:"agree"`
}

// Detect classifies a DC fast charge Rivian didn't bill. Signals, first
// decisive one wins: Rivian's isRivianCharger, the user's own verified
// charge at the same site, then archive stations by distance. At a site
// with more than one network the nearest is the estimate, a peak only
// one candidate could deliver can pick another, and either way the user
// is asked. pos labels where the charge position came from ("open" or
// "last") for the evidence.
func Detect(in DetectInput, pos string) Detection {
	ev := evidence{V: 1, Pos: pos, Candidates: []evidenceCandidate{}}
	groups := groupCandidates(in.Candidates)
	for _, g := range groups {
		ev.Candidates = append(ev.Candidates, evidenceCandidate{Network: g.display, Slug: g.slug, DistM: math.Round(g.nearest.DistM)})
	}
	done := func(d Detection, source string) Detection {
		ev.Source = source
		d.Evidence, _ = json.Marshal(ev)
		return d
	}
	if in.RivianCharger {
		ran := mustNetwork("ran")
		return done(Detection{Query: ran.DisplayName, Network: ran.DisplayName, State: StateEstimated, Confidence: 0.95}, "rivian_charger")
	}
	if in.Site != nil && in.Site.Network != "" && in.Site.DistM <= SiteMemoryRadiusM {
		return done(Detection{Query: in.Site.Network, Network: in.Site.Network, State: StateEstimated, Confidence: 0.9}, "site_memory")
	}
	if len(groups) == 0 {
		return done(Detection{State: StateNeedsConfirmation, Confidence: 0}, "none")
	}

	pick := groups[0] // nearest network
	conf := 0.9
	switch {
	case len(groups) > 1:
		conf = 0.4
		if g, ok := onlyOneCanDeliver(groups, in.PeakKW); ok {
			pick, conf = g, 0.6
		}
	case pick.slug == UnmatchedNetwork.Slug:
		conf = 0.5
	case pick.nearest.DistM > NearRadiusM:
		conf = 0.7
	}
	// The node must agree with itself: neither its network field nor its
	// name may name a different recognised network than the estimate.
	// (Not ChoosePin as-is: the estimate comes from this very node, and
	// an unbranded station name, "WALMART 1234", isn't a disagreement.)
	// NREL codes (SHELL_RECHARGE) are read as names first: underscores
	// defeat NetworkKey's word boundaries.
	node := pick.nearest
	nodeNet := StationQuery(node.Network, "")
	want := NetworkKey(pick.display)
	agree := !conflicts(want, NetworkKey(nodeNet)) && !conflicts(want, NetworkKey(node.Name))
	ev.NodeCheck = &nodeCheck{Key: NetworkKey(nodeNet, node.Name), Agree: agree}
	if !agree && conf > 0.6 {
		conf = 0.6
	}
	state := StateEstimated
	if conf < ConfidentAt {
		state = StateNeedsConfirmation
	}
	return done(Detection{
		Query:      StationQuery(node.Network, node.Name),
		Network:    pick.display,
		State:      state,
		Confidence: conf,
	}, "archive")
}

// conflicts reports two recognised network keys that differ.
func conflicts(a, b string) bool { return a != "" && b != "" && a != b }

type candidateGroup struct {
	slug, display string
	nearest       Candidate
	maxKW         float64 // highest known station power in the group
}

// groupCandidates groups stations within SiteRadiusM by matched
// network, nearest group first.
func groupCandidates(cs []Candidate) []candidateGroup {
	by := map[string]*candidateGroup{}
	var order []string
	for _, c := range cs {
		if c.DistM > SiteRadiusM {
			continue
		}
		n := MatchNetwork(StationQuery(c.Network, c.Name))
		g, ok := by[n.Slug]
		if !ok {
			g = &candidateGroup{slug: n.Slug, display: n.DisplayName, nearest: c}
			by[n.Slug] = g
			order = append(order, n.Slug)
		}
		if c.DistM < g.nearest.DistM {
			g.nearest = c
		}
		if c.MaxPowerKW > g.maxKW {
			g.maxKW = c.MaxPowerKW
		}
	}
	out := make([]candidateGroup, 0, len(order))
	for _, s := range order {
		out = append(out, *by[s])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].nearest.DistM < out[j].nearest.DistM })
	return out
}

// onlyOneCanDeliver returns the one group whose stations could deliver
// the session's peak when every other group's known maximum is below
// it. Unknown maxima never rule a group out.
func onlyOneCanDeliver(groups []candidateGroup, peakKW float64) (candidateGroup, bool) {
	if peakKW <= 0 {
		return candidateGroup{}, false
	}
	var able []candidateGroup
	for _, g := range groups {
		if g.maxKW == 0 || g.maxKW >= peakKW*0.95 {
			able = append(able, g)
		}
	}
	if len(able) == 1 {
		return able[0], true
	}
	return candidateGroup{}, false
}

func mustNetwork(slug string) Network {
	n, ok := networkBySlug(slug)
	if !ok {
		panic("dcfcrates: no network " + slug)
	}
	return n
}
