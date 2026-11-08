package overnight

import (
	"regexp"
	"strings"
)

// NREL's AFDC station_name for a hotel often starts with the company that owns the building ("Imbue Investments Hampton Inn Dalhart"), which
// reads as the name of the hotel. cleanHotelName drops that prefix, and only when it is certain it is one: the text before a known hotel brand
// holds a company word (Investments, LLC, Properties, Hospitality, ...). A brand that merely is not first is not enough: "Waikiki Beach
// Marriott" and "Universal City Hilton" are the hotel's real names. A name with no such prefix comes back unchanged.
var hotelBrands = []string{
	"Holiday Inn Express", "Holiday Inn", "Hampton Inn", "Best Western", "Comfort Suites", "Comfort Inn", "Quality Inn", "Courtyard", "Residence Inn",
	"Fairfield Inn", "Fairfield", "SpringHill Suites", "TownePlace Suites", "Hyatt", "La Quinta", "Days Inn", "Super 8", "Sheraton", "Westin",
	"Embassy Suites", "DoubleTree", "Doubletree", "Homewood Suites", "Home2 Suites", "Candlewood Suites", "Staybridge Suites", "Crowne Plaza",
	"Wyndham", "Radisson", "Ramada", "Motel 6", "Red Roof", "Econo Lodge", "Sleep Inn", "Baymont", "Microtel", "Hotel Indigo", "Four Points",
	"Aloft", "Tru by Hilton", "Cambria", "Drury Inn", "Drury Plaza", "Kimpton", "Omni", "Ritz-Carlton", "Fairmont", "Hilton Garden Inn", "Hilton",
	"Marriott", "AmericInn", "Country Inn", "Candlewood", "Hawthorn", "Howard Johnson", "Travelodge", "Rodeway", "Clarion", "Sonesta", "SureStay",
}

var (
	hotelBrandRE = regexp.MustCompile(`(?i)\b(` + strings.Join(escapeAll(hotelBrands), "|") + `)\b`)
	// ownerWordRE are the words of a company name (not of a place or a hotel).
	ownerWordRE = regexp.MustCompile(`(?i)\b(investments?|investors|llc|l\.l\.c\.?|inc\.?|properties|property|hospitality|holdings?|group|partners|partnership|management|enterprises?|realty|corp\.?|corporation|company|co\.|lp|l\.p\.|ventures?|development|capital|associates)\b`)
)

func escapeAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = regexp.QuoteMeta(s)
	}
	return out
}

// cleanHotelName trims a leading owner-company prefix from an AFDC hotel name; see above.
func cleanHotelName(name string) string {
	name = strings.TrimSpace(name)
	loc := hotelBrandRE.FindStringIndex(name)
	if loc == nil || loc[0] == 0 {
		return name
	}
	if !ownerWordRE.MatchString(name[:loc[0]]) {
		return name
	}
	return strings.TrimSpace(name[loc[0]:])
}
