package rivian

import (
	"bytes"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	goflag "flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The corpus perturbation tool: how a shape seen in production becomes a committed corpus (rule 7: users' data
// never ships, however stripped; only a perturbed, synthetic version of its shape). It lives here, as a test in
// tool mode, because its safety check is the harness itself: it replays the input and the output and writes
// nothing unless both give the same structure at every event. Run it on a file kept outside the repository (in
// your scratchpad; it refuses a path inside the module), commit only the output, and delete the input:
//
//	go test ./internal/rivian -run TestPerturbCorpus -perturb.in=/scratch/day.json -perturb.out=$PWD/internal/rivian/testdata/corpus/<name>.json [-perturb.hold=12,31]
//
// What changes, with factors drawn from crypto/rand and never recorded (the tool is public: a recorded seed
// would let anyone invert the output back to the real day; -perturb.seed exists for tests on synthetic input):
//   - t: each gap between consecutive events is scaled by a factor in [0.7, 1.3], rounded to whole seconds and at
//     least 1 s; events at one instant stay together. -perturb.hold=i,j keeps the gap before event i exactly.
//   - ts: mapped onto the new timeline through the event at that original instant (plus the original offset), so a
//     replay is still a replay and a stale frame still trails.
//   - soc: a new start in [20, 80], each step keeping its sign, scaled by one factor in [0.7, 1.3]; 0.1 precision.
//   - kw, mph: a new level (one factor each in [0.8, 1.2]) and ±5 % per sample; 0 stays 0; 0.1 precision.
//   - odo: rebuilt from the original deltas scaled by the mph factor (monotonic, from 0); 0.01 precision.
//   - kept: kind, rvm, enum, gear, power, cs, status, fields present, and the order; notes are dropped.
//
// The equivalence check (perturbStructure): after every event, the same drive and charge open (and the same
// number opened so far), and the same cached gear, power, charger state and status. A threshold gap that the
// re-spacing crossed (a pause past the split window, a frame past its freshness) changes one of those, so the
// tool refuses and says where; hold that gap, or run again.

var (
	perturbIn   = goflag.String("perturb.in", "", "corpus to perturb (outside the module; tool mode)")
	perturbOut  = goflag.String("perturb.out", "", "where to write the synthetic corpus")
	perturbHold = goflag.String("perturb.hold", "", "comma-separated event indexes whose preceding gap is kept exactly")
	perturbSeed = goflag.Int64("perturb.seed", 0, "for tests on synthetic input only; 0 draws from crypto/rand")
)

// perturbRand is the factor source: crypto/rand unless a test passes a seed.
type perturbRand func() float64

func newPerturbRand(seed int64) perturbRand {
	if seed != 0 {
		r := rand.New(rand.NewSource(seed))
		return r.Float64
	}
	return func() float64 {
		var b [8]byte
		if _, err := crand.Read(b[:]); err != nil {
			panic(err)
		}
		return float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
	}
}

func round(v, step float64) float64 { return math.Round(v/step) * step }

// perturbEvents returns a re-spaced, re-valued copy of events (the input is not modified).
func perturbEvents(events []corpusEvent, rnd perturbRand, hold map[int]bool) []corpusEvent {
	between := func(lo, hi float64) float64 { return lo + rnd()*(hi-lo) }
	out := make([]corpusEvent, len(events))
	at := map[float64]float64{}
	var prevOld, prevNew float64
	for i, e := range events {
		n := e
		gap := e.T - prevOld
		switch {
		case i == 0:
			n.T = 0
		case gap == 0:
			n.T = prevNew
		case hold[i]:
			n.T = prevNew + gap
		default:
			n.T = prevNew + math.Max(1, math.Round(gap*between(0.7, 1.3)))
		}
		if _, seen := at[e.T]; !seen {
			at[e.T] = n.T
		}
		prevOld, prevNew = e.T, n.T
		n.Note = ""
		out[i] = n
	}
	olds := make([]float64, 0, len(at))
	for k := range at {
		olds = append(olds, k)
	}
	sort.Float64s(olds)
	mapTS := func(ts float64) float64 {
		j := sort.SearchFloat64s(olds, ts)
		if j < len(olds) && olds[j] == ts {
			return at[ts]
		}
		if j == 0 {
			return 0
		}
		return at[olds[j-1]] + math.Round(ts-olds[j-1])
	}
	socStart, socScale := between(20, 80), between(0.7, 1.3)
	kwScale, mphScale := between(0.8, 1.2), between(0.8, 1.2)
	level := func(v, scale float64) float64 {
		if v == 0 {
			return 0
		}
		return math.Max(0.1, round(v*scale*between(0.95, 1.05), 0.1))
	}
	var firstSoC, lastOdo, odo float64
	haveSoC, haveOdo := false, false
	for i := range out {
		e := &out[i]
		if e.TS != nil {
			v := mapTS(*e.TS)
			e.TS = &v
		}
		if e.SoC > 0 {
			if !haveSoC {
				firstSoC, haveSoC = e.SoC, true
			}
			e.SoC = math.Min(100, math.Max(0.1, round(socStart+(e.SoC-firstSoC)*socScale, 0.1)))
		}
		if e.KW != nil {
			v := level(*e.KW, kwScale)
			e.KW = &v
		}
		if e.MPH != nil {
			v := level(*e.MPH, mphScale)
			e.MPH = &v
		}
		if e.Odo != nil {
			if haveOdo {
				odo += math.Max(0, *e.Odo-lastOdo) * mphScale
			}
			lastOdo, haveOdo = *e.Odo, true
			v := round(odo, 0.01)
			e.Odo = &v
		}
	}
	return out
}

// perturbStructure is what a replay must keep, event by event.
type perturbStep struct {
	drive, charge         bool
	drives, charges       int
	gear, power, cs, stat string
}

func perturbStructure(t *testing.T, model string, events []corpusEvent) []perturbStep {
	t.Helper()
	r := newReplayRun(model)
	r.clock = corpusEpoch
	r.m.now = func() time.Time { return r.clock }
	var steps []perturbStep
	var drive *liveDrive
	var charge *liveCharge
	nd, nc := 0, 0
	r.onStep = func(i int, e corpusEvent) {
		d, c := r.session()
		if d != nil && d != drive {
			nd++
		}
		if c != nil && c != charge {
			nc++
		}
		drive, charge = d, c
		s := perturbStep{drive: d != nil, charge: c != nil, drives: nd, charges: nc}
		if st := r.cached(); st != nil {
			s.gear, s.power, s.cs, s.stat = st.Gear, st.PowerState, st.ChargerState, st.ChargerStatus
		}
		steps = append(steps, s)
	}
	r.run(t, events)
	return steps
}

// perturbDiff is the first event where two replays differ, or -1.
func perturbDiff(a, b []perturbStep) int {
	for i := range a {
		if i >= len(b) || a[i] != b[i] {
			return i
		}
	}
	if len(b) != len(a) {
		return len(a)
	}
	return -1
}

func writeCorpus(path, model string, events []corpusEvent) error {
	var b bytes.Buffer
	method := "Perturbed from a real shape with TestPerturbCorpus (random factors, not recorded): gaps re-spaced, values regenerated, transitions kept."
	fmt.Fprintf(&b, "{\n  \"_source\": \"synthetic\",\n  \"_method\": %q,\n  \"vehicle\": {\"model\": %q},\n  \"events\": [\n", method, model)
	for i, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		sep := ","
		if i == len(events)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    %s%s\n", line, sep)
	}
	b.WriteString("  ]\n}\n")
	if _, err := loadCorpusBytes(b.Bytes()); err != nil {
		return fmt.Errorf("the output fails the corpus rules: %w", err)
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}

// TestPerturbCorpus is the tool (skipped unless -perturb.in is given).
func TestPerturbCorpus(t *testing.T) {
	if *perturbIn == "" {
		t.Skip("tool mode only: -perturb.in=<corpus outside the module> -perturb.out=<path>")
	}
	if *perturbOut == "" {
		t.Fatal("-perturb.out is required")
	}
	in, err := filepath.Abs(*perturbIn)
	if err != nil {
		t.Fatal(err)
	}
	if root, err := filepath.Abs("../.."); err == nil && strings.HasPrefix(in, root+string(filepath.Separator)) {
		t.Fatalf("-perturb.in is inside the module (%s): keep a real input in your scratchpad, never in a worktree", root)
	}
	raw, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	var c corpus
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatalf("input: %v", err)
	}
	hold := map[int]bool{}
	for _, f := range strings.Split(*perturbHold, ",") {
		if f = strings.TrimSpace(f); f != "" {
			i, err := strconv.Atoi(f)
			if err != nil {
				t.Fatalf("-perturb.hold %q: %v", f, err)
			}
			hold[i] = true
		}
	}
	out := perturbEvents(c.Events, newPerturbRand(*perturbSeed), hold)
	if i := perturbDiff(perturbStructure(t, c.Vehicle.Model, c.Events), perturbStructure(t, c.Vehicle.Model, out)); i >= 0 {
		t.Fatalf("the perturbed corpus replays differently from event %d on (a threshold gap crossed?): hold that gap (-perturb.hold=%d) or run again; nothing written", i, i)
	}
	if err := writeCorpus(*perturbOut, c.Vehicle.Model, out); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d events); now delete the input", *perturbOut, len(out))
}

// The tool on the committed corpora (synthetic, so a seed is fine): every perturbation of them replays the same,
// passes the corpus rules, and keeps no note.
func TestPerturbKeepsStructure(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "corpus", "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no corpora: %v", err)
	}
	for _, f := range files {
		c, err := loadCorpus(f)
		if err != nil {
			t.Fatal(err)
		}
		want := perturbStructure(t, c.Vehicle.Model, c.Events)
		for seed := int64(1); seed <= 5; seed++ {
			out := perturbEvents(c.Events, newPerturbRand(seed), nil)
			if i := perturbDiff(want, perturbStructure(t, c.Vehicle.Model, out)); i >= 0 {
				t.Errorf("%s seed %d: replays differently from event %d", filepath.Base(f), seed, i)
				continue
			}
			path := filepath.Join(t.TempDir(), "out.json")
			if err := writeCorpus(path, c.Vehicle.Model, out); err != nil {
				t.Errorf("%s seed %d: %v", filepath.Base(f), seed, err)
			}
			if b, _ := os.ReadFile(path); bytes.Contains(b, []byte(`"_note"`)) || bytes.Contains(b, []byte("seed")) {
				t.Errorf("%s seed %d: the output keeps a note or names a seed", filepath.Base(f), seed)
			}
		}
	}
	// A crossed threshold is caught: stretch one corpus's gaps 200× (its brief 0 kW pause becomes longer than the
	// charge-split window) and the replay must differ.
	c, err := loadCorpus(filepath.Join("testdata", "corpus", "r2_charge_with_stale_gear.json"))
	if err != nil {
		t.Fatal(err)
	}
	stretched := make([]corpusEvent, len(c.Events))
	copy(stretched, c.Events)
	for i := range stretched {
		stretched[i].T *= 200
		if stretched[i].TS != nil {
			v := *stretched[i].TS * 200
			stretched[i].TS = &v
		}
	}
	if perturbDiff(perturbStructure(t, c.Vehicle.Model, c.Events), perturbStructure(t, c.Vehicle.Model, stretched)) < 0 {
		t.Error("stretching every gap 200x replays the same: the equivalence check can't see a crossed threshold")
	}
}
