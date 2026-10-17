package dcfcrates

// ResolveRateBySlug is ResolveRate for a network the caller already
// identified by slug (the user picked it, or it came from detection
// evidence): the user's override for that slug first, then the built-in
// default. ok is false for a slug that names no built-in network and
// has no override row.
func ResolveRateBySlug(slug string, overrides []NetworkOverride) (ResolvedRate, bool) {
	base, known := networkBySlug(slug)
	for _, ov := range overrides {
		if ov.Slug != slug || ov.GuestRate <= 0 {
			continue
		}
		name := ov.Name
		if known {
			name = base.DisplayName
		}
		return resolvedFromOverride(ov, name), true
	}
	if !known {
		return ResolvedRate{}, false
	}
	return ResolvedRate{
		Slug:          base.Slug,
		DisplayName:   base.DisplayName,
		GuestRate:     base.GuestRate,
		UserRate:      base.GuestRate, // no override, so no membership
		AllMemberRate: base.MemberRateOrGuest(),
		MemberPlan:    base.MemberPlan,
	}, true
}
