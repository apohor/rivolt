package weather

import (
	"encoding/json"
	"fmt"
	"strings"
)

// selfHostedChain is the model chain our server is asked for, best first, merged per hour and per variable (first non-null wins):
//   - ncep_hrrr_conus: NOAA HRRR 3 km, North America, hours out to its horizon. It matches the public default within 0.3 C and keeps its
//     snow hours. NOAA retires it on Nov 30, 2026; after that its series is absent or all null and the merge falls through to the next model;
//   - ncep_gfs013: NOAA GFS 0.11 degree, global, days out: the US beyond HRRR and Alaska;
//   - gem_seamless: ECCC GEM, built from the synced regional and global models: every point, the last fallback.
//
// Preview diff against the public default (what prod serves today) showed GEM alone moves US temperature by 1.3 to 6.1 C and loses the
// default's snow hours in Canada, so GEM is the fallback and not the primary.
const selfHostedChain = "ncep_hrrr_conus,ncep_gfs013,gem_seamless"

// chainModels splits a models parameter; one model (or none) means no merging is needed.
func chainModels(models string) []string {
	var out []string
	for _, m := range strings.Split(models, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// mergeChain turns a multi-model forecast response into the single-model shape the parsers read. With several models the API names each
// series "<variable>_<model>"; this returns the body with, for every variable, one series under the plain name whose value at each time is
// the first non-null one in the order of models. A variable no model answered stays all-null. Single-model bodies come back unchanged.
func mergeChain(body []byte, models []string) ([]byte, error) {
	if len(models) < 2 {
		return body, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("weather: decode: %w", err)
	}
	for _, blockName := range []string{"hourly", "minutely_15"} {
		raw, ok := top[blockName]
		if !ok {
			continue
		}
		var block map[string]json.RawMessage
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, fmt.Errorf("weather: decode %s: %w", blockName, err)
		}
		merged := map[string]json.RawMessage{"time": block["time"]}
		// Every variable any model answered, then each rebuilt from its model series in chain order (so the result never depends on map order).
		bases := map[string]bool{}
		for key := range block {
			for _, m := range models {
				if key != "time" && strings.HasSuffix(key, "_"+m) {
					bases[strings.TrimSuffix(key, "_"+m)] = true
				}
			}
		}
		// Variables that only make sense together are merged as a pair: per time, both come from the first model that has both. Wind speed from
		// one model and direction from another would put a headwind on a wind that no model forecast.
		paired := map[string]bool{}
		for _, pr := range pairedVariables {
			a, b, ok := mergePair(block, models, pr[0], pr[1])
			if !ok {
				continue // no model answered both: each is merged on its own below
			}
			paired[pr[0]], paired[pr[1]] = true, true
			for base, vals := range map[string][]*float64{pr[0]: a, pr[1]: b} {
				enc, err := json.Marshal(vals)
				if err != nil {
					return nil, err
				}
				merged[base] = enc
			}
		}
		for base := range bases {
			if paired[base] {
				continue
			}
			var out []*float64
			for _, m := range models { // best first
				raw, ok := block[base+"_"+m]
				if !ok {
					continue
				}
				var vals []*float64
				if err := json.Unmarshal(raw, &vals); err != nil {
					return nil, fmt.Errorf("weather: decode %s_%s: %w", base, m, err)
				}
				if out == nil {
					out = make([]*float64, len(vals))
				}
				for i := range out {
					if out[i] == nil && i < len(vals) {
						out[i] = vals[i]
					}
				}
			}
			b, err := json.Marshal(out)
			if err != nil {
				return nil, err
			}
			merged[base] = b
		}
		mb, err := json.Marshal(merged)
		if err != nil {
			return nil, err
		}
		top[blockName] = mb
	}
	return json.Marshal(top)
}

// pairedVariables are merged together: see mergeChain.
var pairedVariables = [][2]string{{"wind_speed_10m", "wind_direction_10m"}}

// mergePair returns the two series for which, at every time, both values come from the first model (in chain order) that answers both; a
// time no model answers both for is null in both. ok is false when no model answered either variable.
func mergePair(block map[string]json.RawMessage, models []string, a, b string) (outA, outB []*float64, ok bool) {
	type pair struct{ a, b []*float64 }
	var ps []pair
	n := 0
	for _, m := range models {
		ra, okA := block[a+"_"+m]
		rb, okB := block[b+"_"+m]
		if !okA || !okB {
			continue
		}
		var va, vb []*float64
		if json.Unmarshal(ra, &va) != nil || json.Unmarshal(rb, &vb) != nil {
			continue
		}
		ps = append(ps, pair{va, vb})
		if len(va) > n {
			n = len(va)
		}
	}
	if len(ps) == 0 {
		return nil, nil, false
	}
	outA, outB = make([]*float64, n), make([]*float64, n)
	for i := 0; i < n; i++ {
		for _, p := range ps {
			if i < len(p.a) && i < len(p.b) && p.a[i] != nil && p.b[i] != nil {
				outA[i], outB[i] = p.a[i], p.b[i]
				break
			}
		}
	}
	return outA, outB, true
}
