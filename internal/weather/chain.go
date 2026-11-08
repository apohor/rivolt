package weather

import (
	"encoding/json"
	"fmt"
	"strings"
)

// selfHostedChain is the model chain our server is asked for, best first, merged per hour and per variable (first non-null wins):
//   - ncep_rrfs_conus: NOAA RRFS 3 km, the continental US, hours out to its horizon;
//   - ncep_gfs013: NOAA GFS 0.11 degree, global, days out: the US beyond RRFS and Alaska;
//   - gem_seamless: ECCC GEM, built from the synced regional and global models: every point, the last fallback.
//
// Preview diff against the public default (what prod serves today) showed GEM alone moves US temperature by 1.3 to 6.1 C and loses the
// default's snow hours in Canada, so GEM is the fallback and not the primary.
const selfHostedChain = "ncep_rrfs_conus,ncep_gfs013,gem_seamless"

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
		for base := range bases {
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
