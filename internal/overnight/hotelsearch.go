package overnight

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/apohor/rivolt/internal/chargers"
)

// HotelHit is one hotel of the charger archive that matched a place search: the cleaned name, where it is, and its address.
type HotelHit struct {
	Name    string
	City    string
	State   string // two-letter code
	Country string // two-letter code
	Lat     float64
	Lon     float64
}

// hotelStopWords are the words of a place search that say what kind of place it is, not which: the owner's "hampton inn hotel in dalhart" must
// find a station named "... Hampton Inn Dalhart" that does not say "hotel".
var hotelStopWords = map[string]bool{
	"hotel": true, "hotels": true, "motel": true, "motels": true, "lodging": true, "in": true, "at": true, "near": true, "the": true, "a": true, "and": true, "of": true,
}

type hotelEntry struct {
	hit HotelHit
	// nameOnly are the words of the station's name that are not its city or state: the words that say which hotel it is.
	nameOnly []string
}

// hotelIndex is a token index over the hotel stations: sorted distinct tokens (name, city, state) and the entries each one occurs in.
type hotelIndex struct {
	entries []hotelEntry
	tokens  []string  // sorted, distinct
	posting [][]int32 // posting[i] lists the entries holding tokens[i]
}

// HotelSearch answers name searches over the hotel stations of the charger archive. It keeps one index per loaded copy of the archive, built
// the first time after the archive loads or reloads, so a search never walks the tiles or the whole list.
type HotelSearch struct {
	archive HotelSource
	mu      sync.Mutex
	loaded  time.Time
	idx     *hotelIndex
}

// HotelSource is the charger archive as the search reads it (*chargers.Archive).
type HotelSource interface {
	Hotels() ([]chargers.POI, time.Time)
}

// NewHotelSearch returns a search over archive's hotels (nil archive: always no hits).
func NewHotelSearch(archive *chargers.Archive) *HotelSearch {
	if archive == nil {
		return &HotelSearch{}
	}
	return &HotelSearch{archive: archive}
}

func (h *HotelSearch) index() *hotelIndex {
	pois, at := h.archive.Hotels()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.idx == nil || !at.Equal(h.loaded) {
		h.idx = buildHotelIndex(pois)
		h.loaded = at
	}
	return h.idx
}

func buildHotelIndex(pois []chargers.POI) *hotelIndex {
	ix := &hotelIndex{entries: make([]hotelEntry, 0, len(pois))}
	byTok := map[string][]int32{}
	for _, p := range pois {
		hit := HotelHit{Name: cleanHotelName(p.Name), City: strings.TrimSpace(p.City), State: strings.ToUpper(strings.TrimSpace(p.State)),
			Country: strings.ToUpper(strings.TrimSpace(p.Country)), Lat: p.Lat, Lon: p.Lon}
		e := hotelEntry{hit: hit}
		id := int32(len(ix.entries))
		addr := map[string]bool{}
		for _, t := range tokenize(hit.City + " " + hit.State) {
			addr[t] = true
		}
		seen := map[string]bool{}
		// The raw name is indexed too: the owner prefix of "Imbue Investments Hampton Inn Dalhart" is still a way to find it.
		for _, t := range tokenize(p.Name + " " + hit.City + " " + hit.State) {
			if seen[t] {
				continue
			}
			seen[t] = true
			byTok[t] = append(byTok[t], id)
			if !addr[t] {
				e.nameOnly = append(e.nameOnly, t)
			}
		}
		ix.entries = append(ix.entries, e)
	}
	ix.tokens = make([]string, 0, len(byTok))
	for t := range byTok {
		ix.tokens = append(ix.tokens, t)
	}
	sort.Strings(ix.tokens)
	ix.posting = make([][]int32, len(ix.tokens))
	for i, t := range ix.tokens {
		ix.posting[i] = byTok[t]
	}
	return ix
}

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// maxPrefixTokens bounds how many distinct tokens one query word may expand to ("h" would be most of them).
const maxPrefixTokens = 200

// Search returns up to limit hotels whose name, city and state together hold every word of q (the last word may be a prefix), nearest to
// near first (or the shortest name first when near is nil). Words that only say "hotel" are ignored; a query of nothing else matches nothing.
func (h *HotelSearch) Search(q string, nearLat, nearLon float64, hasNear bool, limit int) []HotelHit {
	if h == nil || h.archive == nil || limit <= 0 {
		return nil
	}
	var words []string
	for _, w := range tokenize(q) {
		if !hotelStopWords[w] {
			words = append(words, w)
		}
	}
	if len(words) == 0 || len(words) > 8 {
		return nil
	}
	ix := h.index()
	var cand map[int32]bool
	for wi, w := range words {
		lo := sort.SearchStrings(ix.tokens, w)
		got := map[int32]bool{}
		n := 0
		for i := lo; i < len(ix.tokens) && strings.HasPrefix(ix.tokens[i], w) && n < maxPrefixTokens; i, n = i+1, n+1 {
			// Only the last word of the query may be a prefix; the others must be whole words.
			if wi != len(words)-1 && ix.tokens[i] != w {
				break
			}
			for _, id := range ix.posting[i] {
				if cand == nil || cand[id] {
					got[id] = true
				}
			}
		}
		cand = got
		if len(cand) == 0 {
			return nil
		}
	}
	type scored struct {
		hit  HotelHit
		dist float64
	}
	out := make([]scored, 0, len(cand))
	for id := range cand {
		e := ix.entries[id]
		// A search for a town ("dumas") is not a search for the hotels in it: at least one word must be one of the hotel's own.
		if !namesHotel(e.nameOnly, words) {
			continue
		}
		d := float64(len(e.hit.Name))
		if hasNear {
			d = haversineMeters(nearLat, nearLon, e.hit.Lat, e.hit.Lon)
		}
		out = append(out, scored{e.hit, d})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].dist != out[j].dist {
			return out[i].dist < out[j].dist
		}
		return out[i].hit.Name < out[j].hit.Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	hits := make([]HotelHit, len(out))
	for i, s := range out {
		hits[i] = s.hit
	}
	return hits
}

// namesHotel reports whether any word of the query is (the start of) a word of the hotel's own name.
func namesHotel(nameOnly, words []string) bool {
	for _, w := range words {
		for _, t := range nameOnly {
			if strings.HasPrefix(t, w) {
				return true
			}
		}
	}
	return false
}

func haversineMeters(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * 6371008.8 * math.Asin(math.Min(1, math.Sqrt(a)))
}
