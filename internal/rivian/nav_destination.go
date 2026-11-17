package rivian

import (
	"encoding/base64"
	"encoding/binary"
	"math"
	"sync"
	"time"
	"unicode/utf8"
)

// Live destination, step 1 (_shared/destination-step1-design.md). A
// destination is a precise location, so everything here is memory only:
// the decoded trip lives in a navStore on the owner pod's monitor and
// nowhere else (not in State, the live-state snapshot, the recorder, the
// archive, a log line or a metric label). Field numbers come from the
// Rivian app's message descriptors (static research, never run); names are
// ours. Coordinates are read as a nested {1 latitude, 2 longitude} pair of
// doubles like the GNSS topic, an inference the first live frames confirm
// (a mismatch shows as dropped_range, not as a wrong pin).

// Hard limits: a frame outside them is dropped whole and counted.
const (
	navMaxFrameBytes = 16 << 10
	navMaxNameBytes  = 200
	navMaxRouteBytes = 8 << 10
	navMaxStops      = 32
	navMaxMetres     = 5_000_000 // 5000 km
	navETAPast       = 2 * time.Minute
	navETAFuture     = 48 * time.Hour
	// navStaleFloor is how long a trip stays shown without a trip_progress
	// frame. The design says max(3 min, 3x the p95 gap); the p95 is read from
	// the count-only counter before this is raised.
	navStaleFloor = 3 * time.Minute
)

// Decode results, the only label values the decode counter ever carries.
const (
	NavResultOK        = "ok"
	NavResultSize      = "dropped_size"
	NavResultMalformed = "dropped_malformed"
	NavResultRange     = "dropped_range"
)

// Clear reasons, the only label values the cleared counter ever carries.
const (
	NavClearEnded     = "nav_ended"
	NavClearDrive     = "drive_ended"
	NavClearReconnect = "reconnect"
	NavClearStale     = "stale"
	NavClearFlagOff   = "flag_off"
	NavClearNewTrip   = "new_destination"
	NavClearStopped   = "stopped"
)

// NavInfo is what we keep of a trip_info frame.
type NavInfo struct {
	Name      string
	Lat, Lon  float64
	Stops     int       // number of stops, the last one is the destination
	LegMetres []float64 // distance of each leg
	Route     string    // encoded polyline, as sent
}

// NavProgress is what we keep of a trip_progress frame.
type NavProgress struct {
	ETA        time.Time // trip ETA
	NextStop   int
	LegMetres  float64 // remaining on the current leg
	HasLegDist bool
}

// NavFrame is one decoded navigation frame: exactly one of Info and
// Progress is set.
type NavFrame struct {
	Info     *NavInfo
	Progress *NavProgress
}

// NavTrip is the combined view served to the owner.
type NavTrip struct {
	Name       string
	Lat, Lon   float64
	ETA        time.Time
	MetresLeft float64
	// HasDist says MetresLeft is the whole way to the destination. Without a leg distance in the frames it would be the later legs only, so a
	// reader must not show it.
	HasDist bool
	Route   string
}

func pbNumber(wire int, val []byte) (float64, bool) {
	switch wire {
	case 0:
		v, n := binary.Uvarint(val)
		if n <= 0 {
			return 0, false
		}
		return float64(v), true
	case 1:
		return math.Float64frombits(binary.LittleEndian.Uint64(val)), true
	case 5:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(val))), true
	}
	return 0, false
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// navFrameError carries the result class of a dropped frame, never content.
type navFrameError string

func (e navFrameError) Error() string { return string(e) }

func pbTimestamp(buf []byte) (time.Time, bool) {
	var secs int64
	var ok bool
	if err := pbScan(buf, func(f, w int, v []byte) {
		if f == 1 && w == 0 {
			u, n := binary.Uvarint(v)
			if n > 0 {
				secs, ok = int64(u), true
			}
		}
	}); err != nil || !ok {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

func pbCoordinate(buf []byte) (lat, lon float64, ok bool) {
	var gotLat, gotLon bool
	if err := pbScan(buf, func(f, w int, v []byte) {
		if w != 1 {
			return
		}
		switch f {
		case 1:
			lat, gotLat = math.Float64frombits(binary.LittleEndian.Uint64(v)), true
		case 2:
			lon, gotLon = math.Float64frombits(binary.LittleEndian.Uint64(v)), true
		}
	}); err != nil {
		return 0, 0, false
	}
	return lat, lon, gotLat && gotLon
}

// decodeNavFrame decodes one navigation payload. The result is one of the
// NavResult constants; on anything but NavResultOK the frame is nil.
func decodeNavFrame(rvm, payloadB64 string, now time.Time) (frame *NavFrame, result string) {
	if base64.StdEncoding.DecodedLen(len(payloadB64)) > navMaxFrameBytes {
		return nil, NavResultSize
	}
	raw, err := base64.StdEncoding.DecodeString(payloadB64)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(payloadB64); err != nil {
			return nil, NavResultMalformed
		}
	}
	defer func() {
		if recover() != nil { // a decoder bug must not take the monitor, nor print the value
			frame, result = nil, NavResultMalformed
		}
	}()
	var f *NavFrame
	switch rvm {
	case rvmNavTripInfo:
		var info *NavInfo
		info, err = decodeNavInfo(raw)
		if err == nil {
			f = &NavFrame{Info: info}
		}
	case rvmNavTripProgress:
		var p *NavProgress
		p, err = decodeNavProgress(raw, now)
		if err == nil {
			f = &NavFrame{Progress: p}
		}
	default:
		return nil, NavResultMalformed
	}
	if err != nil {
		return nil, string(err.(navFrameError))
	}
	return f, NavResultOK
}

func decodeNavProgress(raw []byte, now time.Time) (*NavProgress, error) {
	p := &NavProgress{}
	var etaOK, bad bool
	err := pbScan(raw, func(f, w int, v []byte) {
		switch {
		case f == 2 && w == 2:
			p.ETA, etaOK = pbTimestamp(v)
		case f == 3 && w == 0:
			n, _ := binary.Uvarint(v)
			if n > navMaxStops {
				bad = true
			}
			p.NextStop = int(n)
		case f == 4 && (w == 1 || w == 5):
			d, _ := pbNumber(w, v)
			if !finite(d) || d < 0 || d > navMaxMetres {
				bad = true
			}
			p.LegMetres, p.HasLegDist = d, true
		}
		// 1 leg ETA, 5 leg seconds, 6 the car's GPS: not kept.
	})
	if err != nil {
		return nil, navFrameError(NavResultMalformed)
	}
	if bad || !etaOK || p.ETA.Before(now.Add(-navETAPast)) || p.ETA.After(now.Add(navETAFuture)) {
		return nil, navFrameError(NavResultRange)
	}
	return p, nil
}

func decodeNavInfo(raw []byte) (*NavInfo, error) {
	var route []byte
	if err := pbScan(raw, func(f, w int, v []byte) {
		if f == 3 && w == 2 {
			route = v
		}
	}); err != nil {
		return nil, navFrameError(NavResultMalformed)
	}
	info := &NavInfo{}
	var lastStop []byte
	var lastIsCharge bool
	var tooBig, bad bool
	if err := pbScan(route, func(f, w int, v []byte) {
		switch {
		case f == 3 && w == 2: // stop
			info.Stops++
			if info.Stops > navMaxStops {
				tooBig = true
				return
			}
			_ = pbScan(v, func(sf, sw int, sv []byte) {
				if sw == 2 && (sf == 1 || sf == 2) {
					lastStop, lastIsCharge = sv, sf == 2
				}
			})
		case f == 4 && w == 2: // leg
			if len(info.LegMetres) >= navMaxStops {
				tooBig = true
				return
			}
			var d float64
			_ = pbScan(v, func(lf, lw int, lv []byte) {
				if lf == 1 {
					d, _ = pbNumber(lw, lv)
				}
			})
			if !finite(d) || d < 0 || d > navMaxMetres {
				bad = true
			}
			info.LegMetres = append(info.LegMetres, d)
		case f == 10 && w == 2:
			if len(v) > navMaxRouteBytes {
				tooBig = true
				return
			}
			info.Route = string(v)
		}
	}); err != nil {
		return nil, navFrameError(NavResultMalformed)
	}
	if tooBig {
		return nil, navFrameError(NavResultSize)
	}
	if bad {
		return nil, navFrameError(NavResultRange)
	}
	if info.Stops == 0 {
		return info, nil // no stops: navigation ended
	}
	nameField, coordField := 4, 1
	if lastIsCharge {
		nameField, coordField = 2, 3
	}
	var coord []byte
	var name []byte
	if err := pbScan(lastStop, func(f, w int, v []byte) {
		if w != 2 {
			return
		}
		switch f {
		case nameField:
			name = v
		case coordField:
			coord = v
		}
	}); err != nil {
		return nil, navFrameError(NavResultMalformed)
	}
	if len(name) > navMaxNameBytes {
		return nil, navFrameError(NavResultSize)
	}
	if !utf8.Valid(name) {
		return nil, navFrameError(NavResultMalformed)
	}
	info.Name = string(name)
	lat, lon, ok := pbCoordinate(coord)
	if !ok || !finite(lat) || !finite(lon) || math.Abs(lat) > 90 || math.Abs(lon) > 180 || (lat == 0 && lon == 0) {
		return nil, navFrameError(NavResultRange)
	}
	info.Lat, info.Lon = lat, lon
	return info, nil
}

// navStore holds the current trip per vehicle, in memory only, behind its
// own mutex. It is its own type: not a field of State, LiveDrive or any
// snapshot (TestNavStoreIsNotReachableFromState pins that).
type navStore struct {
	mu    sync.Mutex
	now   func() time.Time
	trips map[string]*navEntry
	// onClear is told the reason of every clear that removed a trip.
	onClear func(reason string)
}

type navEntry struct {
	info   *NavInfo
	infoAt time.Time
	prog   *NavProgress
	progAt time.Time
}

func newNavStore(now func() time.Time) *navStore {
	if now == nil {
		now = time.Now
	}
	return &navStore{now: now, trips: map[string]*navEntry{}}
}

func (s *navStore) setOnClear(f func(reason string)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onClear = f
	s.mu.Unlock()
}

// cleared is called with s.mu held.
func (s *navStore) cleared(reason string) {
	if s.onClear != nil {
		s.onClear(reason)
	}
}

// apply folds one decoded frame in; the active-trip rule is the app's own:
// at least one stop and the next stop inside the list.
func (s *navStore) apply(vehicleID string, f *NavFrame) {
	if s == nil || f == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.trips[vehicleID]
	switch {
	case f.Info != nil:
		if f.Info.Stops == 0 {
			if e != nil {
				delete(s.trips, vehicleID)
				s.cleared(NavClearEnded)
			}
			return
		}
		if e == nil {
			s.trips[vehicleID] = &navEntry{info: f.Info, infoAt: s.now()}
			return
		}
		if e.info.Lat != f.Info.Lat || e.info.Lon != f.Info.Lon {
			e.prog = nil // a progress frame of the old trip must not time the new one
			s.cleared(NavClearNewTrip)
		}
		e.info, e.infoAt = f.Info, s.now()
	case f.Progress != nil:
		if e == nil {
			return // progress without a route is not a trip yet
		}
		if f.Progress.NextStop >= e.info.Stops {
			delete(s.trips, vehicleID)
			s.cleared(NavClearEnded)
			return
		}
		e.prog, e.progAt = f.Progress, s.now()
	}
}

// get returns the trip, or false when none is active or the last progress
// frame is older than navStaleFloor (which clears it).
func (s *navStore) get(vehicleID string) (NavTrip, bool) {
	if s == nil {
		return NavTrip{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.trips[vehicleID]
	if e == nil || e.prog == nil {
		return NavTrip{}, false
	}
	if s.now().Sub(e.progAt) > navStaleFloor {
		delete(s.trips, vehicleID)
		s.cleared(NavClearStale)
		return NavTrip{}, false
	}
	left := e.prog.LegMetres
	for i := e.prog.NextStop + 1; i < len(e.info.LegMetres); i++ {
		left += e.info.LegMetres[i]
	}
	return NavTrip{Name: e.info.Name, Lat: e.info.Lat, Lon: e.info.Lon, ETA: e.prog.ETA, MetresLeft: left, HasDist: e.prog.HasLegDist, Route: e.info.Route}, true
}

// sweep drops every trip whose newest frame is older than navStaleFloor,
// whether or not anyone reads it (a route with no progress frame included).
func (s *navStore) sweep() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, e := range s.trips {
		newest := e.infoAt
		if e.prog != nil && e.progAt.After(newest) {
			newest = e.progAt
		}
		if s.now().Sub(newest) > navStaleFloor {
			delete(s.trips, id)
			s.cleared(NavClearStale)
		}
	}
}

func (s *navStore) clear(vehicleID, reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.trips[vehicleID]; ok {
		delete(s.trips, vehicleID)
		s.cleared(reason)
	}
}

func (s *navStore) clearAll(reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range s.trips {
		delete(s.trips, id)
		s.cleared(reason)
	}
}

// SetNavClockForTest and ApplyNavFrameForTest let the API tests drive the trip store through the mounted route: a trip is fed in, the clock
// moves, and the endpoint's answer is read. Nothing in the server calls them.
func (m *StateMonitor) SetNavClockForTest(now func() time.Time) { m.nav.now = now }

// ApplyNavFrameForTest folds one frame into the trip store, as a decoded drive-dynamics frame would.
func (m *StateMonitor) ApplyNavFrameForTest(vehicleID string, f *NavFrame) { m.nav.apply(vehicleID, f) }
