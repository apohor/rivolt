package api

import (
	"net/http"
	"unicode"
)

// vehicleFilter reads ?vehicle_id= on the drive and charge lists: one of the
// user's trucks by its Rivian vehicle id (the Now card's last drive and last
// charge for the truck on screen). Empty means every truck, as before. The
// stores scope rows to the signed-in user, so another user's id lists
// nothing, the same as an unknown one. Writes a 400 and returns false for an
// id no truck could have.
func vehicleFilter(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.URL.Query().Get("vehicle_id")
	if len(id) > 128 {
		http.Error(w, "vehicle_id is too long", http.StatusBadRequest)
		return "", false
	}
	for _, c := range id {
		if c > unicode.MaxASCII || !(unicode.IsLetter(c) || unicode.IsDigit(c) || c == '-' || c == '_' || c == ':' || c == '.') {
			http.Error(w, "vehicle_id has unexpected characters", http.StatusBadRequest)
			return "", false
		}
	}
	return id, true
}
