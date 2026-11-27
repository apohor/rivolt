package overnight

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/chargers"
)

type fakeHotels struct {
	pois  []chargers.POI
	at    time.Time
	calls atomic.Int32
}

func (f *fakeHotels) Hotels() ([]chargers.POI, time.Time) { f.calls.Add(1); return f.pois, f.at }

func hotelFixture() *fakeHotels {
	return &fakeHotels{at: time.Unix(1000, 0), pois: []chargers.POI{
		{Name: "Imbue Investments Hampton Inn Dalhart", City: "Dalhart", State: "TX", Country: "US", Lat: 36.06, Lon: -102.51},
		{Name: "Hampton Inn Amarillo West", City: "Amarillo", State: "TX", Country: "US", Lat: 35.2, Lon: -101.9},
		{Name: "Hampton Inn Boise", City: "Boise", State: "ID", Country: "US", Lat: 43.6, Lon: -116.2},
		{Name: "Holiday Inn Express Dumas", City: "Dumas", State: "TX", Country: "US", Lat: 35.86, Lon: -101.97},
	}}
}

// The owner's search: the cleaned name, the town and state, and the Dalhart hotel first.
func TestHotelSearchHamptonInnDalhart(t *testing.T) {
	hs := &HotelSearch{archive: hotelFixture()}
	for _, q := range []string{"hampton inn dalhart", "hampton inn hotel in dalhart", "Hampton Inn Dalhart, TX", "hampton inn dalh"} {
		hits := hs.Search(q, 0, 0, false, 5)
		if len(hits) != 1 {
			t.Fatalf("%q: %d hits, want 1: %+v", q, len(hits), hits)
		}
		h := hits[0]
		if h.Name != "Hampton Inn Dalhart" || h.City != "Dalhart" || h.State != "TX" {
			t.Errorf("%q: got %+v, want Hampton Inn Dalhart in Dalhart, TX", q, h)
		}
	}
}

// With a point, the nearest of the same-named hotels is first; a name that is not there finds nothing; words that only say "hotel" match nothing.
func TestHotelSearchRankAndMisses(t *testing.T) {
	hs := &HotelSearch{archive: hotelFixture()}
	hits := hs.Search("hampton inn", 35.3, -101.8, true, 5)
	if len(hits) != 3 || hits[0].City != "Amarillo" || hits[2].City != "Boise" {
		t.Errorf("near Amarillo: %+v, want Amarillo first and Boise last", hits)
	}
	if got := hs.Search("hampton inn", 0, 0, false, 2); len(got) != 2 {
		t.Errorf("limit 2: %d hits", len(got))
	}
	for _, q := range []string{"hampton inn lubbock", "hotel", "the motel in", "", "dalhart hampton imbue investments x"} {
		if got := hs.Search(q, 0, 0, false, 5); len(got) != 0 {
			t.Errorf("%q: %+v, want none", q, got)
		}
	}
}

// The index is built once per loaded copy and again when the archive reloads (a new load time).
func TestHotelSearchRebuildsOnReload(t *testing.T) {
	f := hotelFixture()
	hs := &HotelSearch{archive: f}
	if len(hs.Search("hampton inn lubbock", 0, 0, false, 5)) != 0 {
		t.Fatal("lubbock should not exist yet")
	}
	first := hs.idx
	hs.Search("hampton inn dalhart", 0, 0, false, 5)
	if hs.idx != first {
		t.Error("the index was rebuilt without a reload")
	}
	f.pois = append(f.pois, chargers.POI{Name: "Hampton Inn Lubbock", City: "Lubbock", State: "TX", Lat: 33.5, Lon: -101.9})
	f.at = f.at.Add(time.Hour)
	if len(hs.Search("hampton inn lubbock", 0, 0, false, 5)) != 1 {
		t.Error("the reloaded archive's hotel was not found")
	}
}

func TestHotelSearchNilArchive(t *testing.T) {
	if got := NewHotelSearch(nil).Search("hampton inn", 0, 0, false, 5); got != nil {
		t.Errorf("nil archive: %+v", got)
	}
	var hs *HotelSearch
	if got := hs.Search("x", 0, 0, false, 5); got != nil {
		t.Error("nil search")
	}
}

// A search for a town is not a search for the hotels in it, even when the hotel's name carries the town.
func TestHotelSearchTownAloneFindsNoHotel(t *testing.T) {
	hs := &HotelSearch{archive: hotelFixture()}
	for _, q := range []string{"dumas", "dumas tx", "dalhart, tx", "amarillo"} {
		if got := hs.Search(q, 0, 0, false, 5); len(got) != 0 {
			t.Errorf("%q: %+v, want none", q, got)
		}
	}
	if got := hs.Search("holiday dumas", 0, 0, false, 5); len(got) != 1 {
		t.Errorf("holiday dumas: %+v, want the Dumas Holiday Inn Express", got)
	}
}
