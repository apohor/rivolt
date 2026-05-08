package drives

// IsReparking reports whether a drive looks like an ignition cycle
// or in-place gear bounce rather than real travel: zero or near-zero
// distance with no SoC delta. The recorder still emits these so the
// raw timeline is preserved; the listing endpoints filter them out
// so the user doesn't see noise rows alongside real drives.
//
// Threshold: 0.05 mi (~80 m) covers the GPS jitter envelope of a
// vehicle parked at a charger stall while still letting any real
// short drive (turn around the block, ~0.1 mi minimum) through.
func IsReparking(d Drive) bool {
	if d.DistanceMi >= 0.05 {
		return false
	}
	return d.StartSoCPct == d.EndSoCPct
}

// FilterReparking returns ds with reparking rows removed. Order is
// preserved; the slice is rebuilt rather than compacted in place so
// callers don't need to worry about reusing the input.
func FilterReparking(ds []Drive) []Drive {
	out := make([]Drive, 0, len(ds))
	for _, d := range ds {
		if IsReparking(d) {
			continue
		}
		out = append(out, d)
	}
	return out
}
