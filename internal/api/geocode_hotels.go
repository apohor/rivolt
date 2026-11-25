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
	for _, h := range hits {
		name := h.Name
		if h.City != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(h.City)) {
			name += ", " + h.City
		}
		r := geocoding.Result{Name: name, Latitude: h.Lat, Longitude: h.Lon, Admin1: h.State, Locality: h.City, EVCharging: true}
		if cn := countryNames[h.Country]; cn != "" {
			r.Country, r.CountryCode = cn, strings.ToLower(h.Country)
		}
		out = append(out, r)
	}
	return out
}

// mergeHotelResults puts the charger-archive hotels first and then the Photon rows, dropping a Photon row that is the same hotel (the same name
// within sameHotelM): the charger row wins and carries the EV charging flag. At most count rows.
func mergeHotelResults(hotels, photon []geocoding.Result, count int) []geocoding.Result {
	if len(hotels) == 0 {
		return photon
	}
	out := append([]geocoding.Result{}, hotels...)
	for _, p := range photon {
		if sameHotelRow(hotels, p) {
			continue
		}
		out = append(out, p)
	}
	if count > 0 && len(out) > count {
		out = out[:count]
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
