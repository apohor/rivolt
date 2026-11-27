package api

import (
	"strings"

	"github.com/apohor/rivolt/internal/geocoding"
	"github.com/apohor/rivolt/internal/overnight"
)

// HotelNameSearcher finds hotels by name in the charger archive (*overnight.HotelSearch).
type HotelNameSearcher interface {
	Search(q string, nearLat, nearLon float64, hasNear bool, limit int) []overnight.HotelHit
}

// hotelHitsKept is how many charger-archive hotels a place search can add; they come before the OpenStreetMap rows.
const hotelHitsKept = 3

// sameHotelM is how close a Photon row and a charger-archive hotel of the same name are to be one hotel.
const sameHotelM = 150.0

// usStateNames spells a US state code the way the OpenStreetMap place rows do ("TX" is "Texas"), so both kinds of row read alike.
var usStateNames = map[string]string{"AL": "Alabama", "AK": "Alaska", "AZ": "Arizona", "AR": "Arkansas", "CA": "California", "CO": "Colorado", "CT": "Connecticut", "DE": "Delaware", "DC": "District of Columbia", "FL": "Florida", "GA": "Georgia", "HI": "Hawaii", "ID": "Idaho", "IL": "Illinois", "IN": "Indiana", "IA": "Iowa", "KS": "Kansas", "KY": "Kentucky", "LA": "Louisiana", "ME": "Maine", "MD": "Maryland", "MA": "Massachusetts", "MI": "Michigan", "MN": "Minnesota", "MS": "Mississippi", "MO": "Missouri", "MT": "Montana", "NE": "Nebraska", "NV": "Nevada", "NH": "New Hampshire", "NJ": "New Jersey", "NM": "New Mexico", "NY": "New York", "NC": "North Carolina", "ND": "North Dakota", "OH": "Ohio", "OK": "Oklahoma", "OR": "Oregon", "PA": "Pennsylvania", "RI": "Rhode Island", "SC": "South Carolina", "SD": "South Dakota", "TN": "Tennessee", "TX": "Texas", "UT": "Utah", "VT": "Vermont", "VA": "Virginia", "WA": "Washington", "WV": "West Virginia", "WI": "Wisconsin", "WY": "Wyoming"}

var countryNames = map[string]string{"US": "United States", "CA": "Canada"}

// hotelResults returns the charger-archive hotels that match q as search rows, labelled like the OpenStreetMap place rows (the name, and the
// town unless the name already says it), each marked EVCharging.
func hotelResults(hs HotelNameSearcher, q string, near *geocoding.Near, limit int) []geocoding.Result {
	if hs == nil {
		return nil
	}
	var lat, lon float64
	if near != nil {
		lat, lon = near.Lat, near.Lon
	}
	hits := hs.Search(q, lat, lon, near != nil, limit)
	out := make([]geocoding.Result, 0, len(hits))
	oneWord := len(strings.Fields(q)) == 1
	for _, h := range hits {
		// A hotel with no town on record (the charger archive before its city/state tile run) cannot be told from the town a one-word
		// search is after: "dumas" would list hotels named for the town ahead of the town itself.
		if oneWord && h.City == "" {
			continue
		}
		name := h.Name
		if h.City != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(h.City)) {
			name += ", " + h.City
		}
		r := geocoding.Result{Name: name, Latitude: h.Lat, Longitude: h.Lon, Admin1: h.State, Locality: h.City, EVCharging: true}
		if h.Country == "US" || h.Country == "" {
			if sn := usStateNames[strings.ToUpper(h.State)]; sn != "" {
				r.Admin1 = sn
			}
		}
		if cn := countryNames[h.Country]; cn != "" {
			r.Country, r.CountryCode = cn, strings.ToLower(h.Country)
		}
		out = append(out, r)
	}
	return out
}

// mergeHotelResults puts the charger-archive hotels first and then the Photon rows, dropping a Photon row that is the same hotel (the same name
// within sameHotelM): the charger row wins and carries the EV charging flag. The Photon rows are all kept, so up to hotelHitsKept rows
// come on top of count: cutting to count would drop the plain rows at the tail (the far namesake a biased search works in, "Boston, MA").
func mergeHotelResults(hotels, photon []geocoding.Result) []geocoding.Result {
	if len(hotels) == 0 {
		return photon
	}
	// The city typed in full stays among the first three rows, hotels or not: it keeps its place in the Photon list, pushed down by the
	// hotels in front of it but never past the third row ("boston" lists Boston, MA, not three hotels named for it and then the city).
	var exact *geocoding.Result
	at := 0
	for i := range photon {
		if photon[i].Exact {
			exact, at = &photon[i], i
			break
		}
	}
	out := make([]geocoding.Result, 0, len(hotels)+len(photon))
	// A nearby namesake ahead of it in the Photon list (Springfield, MO before Springfield, IL) keeps its place above the hotels, so the far
	// city never lands above the near one.
	lifted := 0
	if exact != nil {
		for i := 0; i < at; i++ {
			if photon[i].Settlement && !sameHotelRow(hotels, photon[i]) {
				out = append(out, photon[i])
				lifted++
			}
		}
	}
	out = append(out, hotels...)
	for i := range photon {
		if exact != nil && i == at {
			continue
		}
		if exact != nil && i < at && photon[i].Settlement && !sameHotelRow(hotels, photon[i]) {
			continue
		}
		if sameHotelRow(hotels, photon[i]) {
			continue
		}
		out = append(out, photon[i])
	}
	if exact != nil {
		pos := min(max(at+len(hotels), lifted), 2, len(out))
		out = append(out[:pos], append([]geocoding.Result{*exact}, out[pos:]...)...)
	}
	return out
}

func sameHotelRow(hotels []geocoding.Result, p geocoding.Result) bool {
	pn := hotelNameKey(p.Name, p.Locality)
	for _, h := range hotels {
		if hotelNameKey(h.Name, h.Locality) == pn && haversineMetres(h.Latitude, h.Longitude, p.Latitude, p.Longitude) <= sameHotelM {
			return true
		}
	}
	return false
}

// hotelNameKey is a row's name without its town (the ", town" label suffix, or a town the name itself ends with: "Hampton Inn Dalhart" and
// OpenStreetMap's "Hampton Inn" in Dalhart are the same hotel), lower-cased with single spaces.
func hotelNameKey(name, town string) string {
	key := strings.Join(strings.Fields(strings.ToLower(name)), " ")
	t := strings.Join(strings.Fields(strings.ToLower(town)), " ")
	if t == "" {
		return key
	}
	for _, suf := range []string{", " + t, " " + t} {
		if strings.HasSuffix(key, suf) {
			return strings.TrimSuffix(key, suf)
		}
	}
	return key
}
