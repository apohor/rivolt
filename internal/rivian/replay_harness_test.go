package rivian

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The replay harness: a corpus of vehicleState and Parallax frames (testdata/corpus/*.json) pushed through the
// StateMonitor's real paths, on a clock pinned to the corpus, with the sessions and the cached State observed
// after every event. Sprint 8 item 1; the corpus rules are DA's (Oct 23) and are enforced here, by the loader
// and by TestCorpusRules over every file, not by review:
//   - no coordinates: the schema has no latitude, longitude, gnss or heading field, and unknown keys are refused;
//   - relative time: the first event is t=0 and every t and ts is seconds from it (mapped onto corpusEpoch);
//   - a re-based odometer: "odo" is miles from the first event, laid onto corpusOdoKm;
//   - "_source" is "synthetic" or "operator", with "_method" saying how. A synthetic corpus may be derived in shape
//     from a real day but its values are perturbed (TestPerturbCorpus), so none is a real user's (privacy.md has no
//     carve-out for anonymized data in a public repo); "operator" is the operator's own vehicles or test accounts;
//   - the vehicle is always replayVehicle.

// replayVehicle is the only vehicle id a corpus run uses.
const replayVehicle = "veh-1"

// corpusEpoch is where t=0 lands: a fixed synthetic instant, never a real one.
var corpusEpoch = time.Date(2030, 1, 7, 9, 0, 0, 0, time.UTC)

// corpusOdoKm is the synthetic odometer start (deltas kept, absolute values not).
const corpusOdoKm = 10000.0

// corpusMaxT bounds t and ts (seconds): a corpus covers at most two days, so an absolute epoch can't pass for one.
const corpusMaxT = 2 * 24 * 3600

type corpus struct {
	Source  string         `json:"_source"`
	Method  string         `json:"_method"`
	Vehicle corpusVehicle  `json:"vehicle"`
	Events  []corpusEvent  `json:"events"`
}

type corpusVehicle struct {
	Model string `json:"model"` // R1T | R1S | R2
}

// corpusEvent is one frame. kind "vs" is a vehicleState push (only the fields given are set, as a push carries
// them); kind "px" is a Parallax drive-dynamics frame: an enum topic with "enum", or a structured topic with
// "fields", which a per-topic encoder (pxEncoders) writes as the protobuf payload so the real decoder runs.
type corpusEvent struct {
	T    float64 `json:"t"`
	Kind string  `json:"kind"`
	Note string  `json:"_note,omitempty"`

	Gear   string   `json:"gear,omitempty"`
	Power  string   `json:"power,omitempty"`
	MPH    *float64 `json:"mph,omitempty"`
	Odo    *float64 `json:"odo,omitempty"` // miles from the first event
	CS     string   `json:"cs,omitempty"`
	Status string   `json:"status,omitempty"`
	KW     *float64 `json:"kw,omitempty"`
	SoC    float64  `json:"soc,omitempty"`

	RVM    string             `json:"rvm,omitempty"`
	Enum   *uint64            `json:"enum,omitempty"`
	TS     *float64           `json:"ts,omitempty"` // the Parallax event time; a ts seen before is the cloud's replay
	Fields map[string]float64 `json:"fields,omitempty"`
}

// pxEncoders writes a structured topic's payload (base64 protobuf) from a corpus event's fields. A topic a
// corpus uses must have one: there is no silent skip. Items add theirs with their corpora (tires, closures…).
var pxEncoders = map[string]func(fields map[string]float64) (string, error){}

func loadCorpus(path string) (*corpus, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := loadCorpusBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// loadCorpusBytes decodes and checks a corpus.
func loadCorpusBytes(raw []byte) (*corpus, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c corpus
	if err := dec.Decode(&c); err != nil {
		return nil, err
	}
	if err := c.check(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *corpus) check() error {
	switch c.Source {
	case "synthetic", "operator":
	default:
		return fmt.Errorf(`_source %q: want "synthetic" or "operator" (a real user's day, even anonymized, is not committed)`, c.Source)
	}
	if strings.TrimSpace(c.Method) == "" {
		return fmt.Errorf("_method is empty: say how the corpus was made")
	}
	switch c.Vehicle.Model {
	case "R1T", "R1S", "R2":
	default:
		return fmt.Errorf("vehicle.model %q", c.Vehicle.Model)
	}
	if len(c.Events) == 0 || c.Events[0].T != 0 {
		return fmt.Errorf("the first event must be at t=0 (times are offsets from it)")
	}
	firstOdo := true
	for i, e := range c.Events {
		if e.T < 0 || e.T > corpusMaxT || (i > 0 && e.T < c.Events[i-1].T) {
			return fmt.Errorf("event %d: t=%v (offsets in seconds, non-decreasing, at most %d)", i, e.T, corpusMaxT)
		}
		if e.TS != nil && (*e.TS < 0 || *e.TS > corpusMaxT) {
			return fmt.Errorf("event %d: ts=%v is not an offset", i, *e.TS)
		}
		if e.Odo != nil {
			if firstOdo && *e.Odo != 0 {
				return fmt.Errorf("event %d: the first odo is %v; it must be 0 (miles from the first event)", i, *e.Odo)
			}
			firstOdo = false
		}
		switch e.Kind {
		case "vs":
			if e.RVM != "" || e.Enum != nil || e.TS != nil || e.Fields != nil {
				return fmt.Errorf("event %d: a vs event carries Parallax fields", i)
			}
		case "px":
			if e.RVM == "" || e.TS == nil || (e.Enum == nil) == (e.Fields == nil) {
				return fmt.Errorf("event %d: a px event needs rvm, ts and exactly one of enum or fields", i)
			}
			if e.Fields != nil && pxEncoders[e.RVM] == nil {
				return fmt.Errorf("event %d: no encoder for %s", i, e.RVM)
			}
		default:
			return fmt.Errorf("event %d: kind %q", i, e.Kind)
		}
	}
	return nil
}

// replayRun is a StateMonitor for one vehicle with the harness's helpers. newReplayRun leaves the monitor on
// the wall clock (unit tests push their own times); replayCorpus pins it to the corpus.
type replayRun struct {
	m      *StateMonitor
	clock  time.Time
	ts     map[string]int64
	onStep func(i int, e corpusEvent)
}

func newReplayRun(model string) *replayRun {
	m := NewStateMonitor(nil, nil)
	m.parallaxDriveDynamics = true
	m.vehicleInfo[replayVehicle] = &Vehicle{ID: replayVehicle, Model: model}
	return &replayRun{m: m, ts: map[string]int64{}}
}

// replayCorpus loads testdata/corpus/<name>.json and returns a run on its clock; call run to play it.
func replayCorpus(t *testing.T, name string) (*replayRun, []corpusEvent) {
	t.Helper()
	c, err := loadCorpus(filepath.Join("testdata", "corpus", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	r := newReplayRun(c.Vehicle.Model)
	r.clock = corpusEpoch
	r.m.now = func() time.Time { return r.clock }
	return r, c.Events
}

// at is a corpus offset on the pinned clock.
func (r *replayRun) at(sec float64) time.Time {
	return corpusEpoch.Add(time.Duration(sec * float64(time.Second)))
}

// run plays the events in order. A vs event is one push with only its fields set (as r2_frames_test did:
// speed goes to the cache first, the way an R2's Parallax gnss supplies it); a px event is one frame, whose
// TimestampMs is its ts on the pinned clock, so a repeated ts is a replay and an old one is stale.
func (r *replayRun) run(t *testing.T, events []corpusEvent) {
	t.Helper()
	ctx := context.Background()
	for i, e := range events {
		r.clock = r.at(e.T)
		switch e.Kind {
		case "vs":
			if e.MPH != nil {
				r.m.mu.Lock()
				if c := r.m.cache[replayVehicle]; c != nil {
					next := *c
					next.SpeedKph = *e.MPH / kphToMi
					r.m.cache[replayVehicle] = &next
				}
				r.m.mu.Unlock()
			}
			st := &State{VehicleID: replayVehicle, At: r.clock, Gear: e.Gear, PowerState: e.Power,
				ChargerState: e.CS, ChargerStatus: e.Status, BatteryLevelPct: e.SoC}
			if e.MPH != nil {
				st.SpeedKph = *e.MPH / kphToMi
			}
			if e.Odo != nil {
				st.OdometerKm = corpusOdoKm + *e.Odo/kmToMi
			} else if r.cached() == nil {
				st.OdometerKm = corpusOdoKm
			}
			if e.KW != nil {
				st.ChargerPowerKW = *e.KW
			}
			r.m.applyVehicleStatePush(ctx, replayVehicle, st)
		case "px":
			f := DriveDynamicsFrame{RVM: e.RVM, TimestampMs: r.at(*e.TS).UnixMilli()}
			if e.Enum != nil {
				f.Value, f.ValueOK = *e.Enum, true
			} else {
				p, err := pxEncoders[e.RVM](e.Fields)
				if err != nil {
					t.Fatalf("event %d: encode %s: %v", i, e.RVM, err)
				}
				f.Payload = p
			}
			r.m.onDriveDynamicsFrame(ctx, replayVehicle, f)
		}
		if r.onStep != nil {
			r.onStep(i, e)
		}
	}
}

func (r *replayRun) cached() *State {
	r.m.mu.RLock()
	defer r.m.mu.RUnlock()
	return r.m.cache[replayVehicle]
}

func (r *replayRun) session() (drive *liveDrive, charge *liveCharge) {
	r.m.sessMu.Lock()
	defer r.m.sessMu.Unlock()
	s := r.m.sessions[replayVehicle]
	if s == nil {
		return nil, nil
	}
	return s.drive, s.charge
}

// replaySessions is the open and close offsets (t) of every drive and charge a corpus run saw.
type replaySessions struct {
	Drives, Charges [][2]float64 // [open t, close t]; close is -1 while still open
}

// trackSessions records sessions as they open and close (chaining any onStep already set).
func (r *replayRun) trackSessions() *replaySessions {
	out := &replaySessions{}
	var drive *liveDrive
	var charge *liveCharge
	prev := r.onStep
	r.onStep = func(i int, e corpusEvent) {
		d, c := r.session()
		if d != drive {
			if drive != nil {
				out.Drives[len(out.Drives)-1][1] = e.T
			}
			if d != nil {
				out.Drives = append(out.Drives, [2]float64{e.T, -1})
			}
			drive = d
		}
		if c != charge {
			if charge != nil {
				out.Charges[len(out.Charges)-1][1] = e.T
			}
			if c != nil {
				out.Charges = append(out.Charges, [2]float64{e.T, -1})
			}
			charge = c
		}
		if prev != nil {
			prev(i, e)
		}
	}
	return out
}

// corpusTextBad is what no string in a corpus (_method, _note) may hold: a Rivian vehicle id, a UUID, a VIN's
// shape, or a decimal with three or more places (a coordinate). Free text is for the reader, not for data.
var corpusTextBad = []*regexp.Regexp{
	regexp.MustCompile(`\b\d{2}-\d{9}\b`),
	regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`),
	regexp.MustCompile(`\b[A-HJ-NPR-Z0-9]{17}\b`),
	regexp.MustCompile(`-?\d{1,3}\.\d{3,}`),
}

// corpusStrings is every string value in a decoded JSON document.
func corpusStrings(v any, out *[]string) {
	switch x := v.(type) {
	case string:
		*out = append(*out, x)
	case []any:
		for _, e := range x {
			corpusStrings(e, out)
		}
	case map[string]any:
		for _, e := range x {
			corpusStrings(e, out)
		}
	}
}

// TestCorpusRules holds every committed corpus to DA's rules: the loader's checks, a raw scan for
// coordinate-like keys (in case a field is ever added to the schema without thinking), and every string value
// (notes, the method) short and free of ids and coordinates.
func TestCorpusRules(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "corpus", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpora: %v", err)
	}
	for _, f := range files {
		if _, err := loadCorpus(f); err != nil {
			t.Error(err)
		}
		raw, _ := os.ReadFile(f)
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		var texts []string
		corpusStrings(doc, &texts)
		for _, s := range texts {
			if len(s) > 160 {
				t.Errorf("%s: a string of %d chars (at most 160): %.60q…", f, len(s), s)
			}
			for _, re := range corpusTextBad {
				if m := re.FindString(s); m != "" {
					t.Errorf("%s: %q holds %q (an id, VIN or coordinate shape)", f, s, m)
				}
			}
		}
		lower := strings.ToLower(string(raw))
		for _, k := range []string{`"lat`, `"lon`, `"lng`, `"gnss`, `"gps`, `"heading`, `"vin`, `"vehicle_id`, `"vehicleid`, `"ssid`, `"address`} {
			if strings.Contains(lower, k) {
				t.Errorf("%s: a %s…\" key: corpora carry no positions, ids or names", f, k)
			}
		}
	}
}

// A zero-value monitor (production: nobody sets now) reads the wall clock.
func TestMonitorClockDefaultsToWallTime(t *testing.T) {
	var m StateMonitor
	if d := time.Since(m.clock()); d < 0 || d > time.Second {
		t.Errorf("clock() is %v off the wall clock", d)
	}
	pinned := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return pinned }
	if !m.clock().Equal(pinned) || m.since(pinned.Add(-time.Minute)) != time.Minute {
		t.Error("a set now isn't used")
	}
}

// An R1 day (synthetic): park and sleep come from vehicleState. One drive, from D to P; one charge, from the
// plug (an R1 reports chargerStatus, which decides plugged) to charging_complete; asleep at the end.
func TestCorpusR1Day(t *testing.T) {
	r, events := replayCorpus(t, "r1_day")
	got := r.trackSessions()
	r.run(t, events)

	if len(got.Drives) != 1 || got.Drives[0][0] != 1890 || got.Drives[0][1] != 3170 {
		t.Errorf("drives %v, want one from t=1890 (D) to t=3170 (P)", got.Drives)
	}
	if len(got.Charges) != 1 || got.Charges[0][0] != 3300 || got.Charges[0][1] != 10800 {
		t.Errorf("charges %v, want one from t=3300 (plugged, status connected) to t=10800 (charging_complete)", got.Charges)
	}
	if c := r.cached(); c.Gear != "P" || c.PowerState != "sleep" {
		t.Errorf("at the end: gear %q power %q, want P and sleep", c.Gear, c.PowerState)
	}
}
