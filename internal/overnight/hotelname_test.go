package overnight

import (
	"testing"

	"github.com/apohor/rivolt/internal/chargers"
)

func TestCleanHotelName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// An owner company before the brand: trimmed.
		{"Imbue Investments Hampton Inn Dalhart", "Hampton Inn Dalhart"},
		{"Imbue Investments Hampton Inn Port Lavaca", "Hampton Inn Port Lavaca"},
		{"Smith Hospitality LLC Holiday Inn Express Amarillo", "Holiday Inn Express Amarillo"},
		{"Lone Star Properties, Inc. Best Western Plus Abilene", "Best Western Plus Abilene"},
		{"  Acme Holdings Courtyard Marriott Tulsa  ", "Courtyard Marriott Tulsa"},
		// The brand is first, or the text before it is part of the hotel's name: unchanged.
		{"Hampton Inn Dalhart", "Hampton Inn Dalhart"},
		{"Waikiki Beach Marriott", "Waikiki Beach Marriott"},
		{"Universal City Hilton", "Universal City Hilton"},
		{"Courtyard Marriott - Oxnard Ventura", "Courtyard Marriott - Oxnard Ventura"},
		{"Mark Hopkins InterContinental Hotel", "Mark Hopkins InterContinental Hotel"},
		// A company word with no known brand after it: unchanged (we do not guess the hotel's name).
		{"Imbue Investments Dalhart", "Imbue Investments Dalhart"},
		{"Palace Hotel", "Palace Hotel"},
		{"", ""},
	} {
		if got := cleanHotelName(c.in); got != c.want {
			t.Errorf("cleanHotelName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// An archive hotel whose AFDC name starts with its owner company is suggested under the hotel's own name; the others keep theirs, and the source
// label stays AFDC.
func TestSuggestedHotelNamesDropTheOwnerPrefix(t *testing.T) {
	a := &fakeArchive{hotels: []chargers.POI{
		hotel("Imbue Investments Hampton Inn Dalhart", 800, 0.5, 11.5),
		hotel("Waikiki Beach Marriott", 801, 0.5, 11.5),
	}}
	res, err := Suggest(a, Request{Path: route(1000), DriveSec: 10 * 3600, BudgetHours: 8})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range res.Candidates {
		got[c.Name] = true
	}
	if !got["Hampton Inn Dalhart"] || !got["Waikiki Beach Marriott"] || got["Imbue Investments Hampton Inn Dalhart"] || len(res.Candidates) != 2 {
		t.Errorf("candidate names = %v", got)
	}
}
