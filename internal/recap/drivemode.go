package recap

import "strings"

// humanizeDriveMode maps Rivian's wire-format drive_mode values to the
// human-readable labels the SPA shows in the live panel. Mirrors the
// formatDriveMode map in web/src/components/LivePanel.tsx; both must
// move together so the LLM recap and the UI describe the same trip in
// the same vocabulary. Unknown values fall through to a Title-cased
// version of the raw input (so "off_road_dev_secret" still reads
// reasonably).
func humanizeDriveMode(raw string) string {
	v := strings.TrimSpace(strings.ToLower(raw))
	if v == "" {
		return ""
	}
	switch v {
	case "everyday":
		return "All-Purpose"
	case "sport":
		return "Sport"
	case "distance":
		return "Conserve"
	case "winter":
		return "Snow"
	case "towing":
		return "Towing"
	case "off_road_auto":
		return "All-Terrain"
	case "off_road_sand":
		return "Soft Sand"
	case "off_road_rocks":
		return "Rock Crawl"
	case "off_road_sport_auto":
		return "Rally"
	case "off_road_sport_drift":
		return "Drift"
	}
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
