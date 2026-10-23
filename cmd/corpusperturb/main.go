// Command corpusperturb turns a replay corpus into a synthetic one: the same transitions in the same order, with
// every time gap re-spaced and every value regenerated, so none of it is a real vehicle's. It is how a shape seen
// in production becomes a committed corpus (internal/rivian/testdata/corpus; users' data never ships, however
// stripped). Run it on a file kept outside the repository and commit only its output:
//
//	go run ./cmd/corpusperturb -in /path/outside/repo.json -seed 7 [-hold 12,31] > internal/rivian/testdata/corpus/<name>.json
//
// What changes and what stays:
//   - t: each gap between consecutive events is scaled by a seeded factor in [0.7, 1.3], rounded to whole
//     seconds and never below 1 s (a 0 gap stays 0: events at the same instant stay together). -hold i,j keeps
//     the gap before event i (and j…) exactly, for a gap a test depends on (a pause shorter than a split window).
//   - ts (a Parallax event time): mapped onto the new timeline through the event at that original time, so a ts
//     seen before is still a replay and a stale ts is still older than the frame it trails.
//   - soc: a new start in [20, 80]; each step keeps its sign, scaled by one seeded factor in [0.7, 1.3].
//   - kw, mph: each a new level (one seeded factor in [0.8, 1.2]) with ±5 % noise per sample; 0 stays 0.
//   - odo: rebuilt from the original deltas scaled by the mph factor, so it stays monotonic and starts at 0.
//   - kept as they are: kind, rvm, enum, gear, power, cs, status, which fields each event has, and the order.
//   - _note is dropped (a note can carry real figures); -keepnotes keeps them for review, never for commit.
//
// The output says it is synthetic, with the seed. A test pinned on the corpus re-derives its expected figures from
// the output; run the harness tests on it before committing.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
)

type event map[string]any

func main() {
	in := flag.String("in", "", "corpus to perturb (keep it outside the repository)")
	seed := flag.Int64("seed", 1, "seed (recorded in the output's _method)")
	hold := flag.String("hold", "", "comma-separated event indexes whose preceding gap is kept exactly")
	keepNotes := flag.Bool("keepnotes", false, "keep _note (for review only; notes can carry real figures)")
	flag.Parse()
	if *in == "" {
		fmt.Fprintln(os.Stderr, "usage: corpusperturb -in <corpus.json> [-seed N] [-hold i,j] > out.json")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		fail(err)
	}
	out, err := perturb(raw, *seed, parseHold(*hold), *keepNotes)
	if err != nil {
		fail(err)
	}
	if _, err := os.Stdout.Write(out); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "corpusperturb:", err)
	os.Exit(1)
}

func parseHold(s string) map[int]bool {
	out := map[int]bool{}
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		i, err := strconv.Atoi(f)
		if err != nil {
			fail(fmt.Errorf("-hold %q: %w", f, err))
		}
		out[i] = true
	}
	return out
}

func num(e event, k string) (float64, bool) {
	v, ok := e[k].(float64)
	return v, ok
}

func perturb(raw []byte, seed int64, hold map[int]bool, keepNotes bool) ([]byte, error) {
	var c struct {
		Vehicle json.RawMessage `json:"vehicle"`
		Events  []event         `json:"events"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if len(c.Events) == 0 {
		return nil, fmt.Errorf("no events")
	}
	rng := rand.New(rand.NewSource(seed))
	between := func(lo, hi float64) float64 { return lo + rng.Float64()*(hi-lo) }

	// Times: re-space each gap; remember where every original instant went, for ts.
	newT := make([]float64, len(c.Events))
	at := map[float64]float64{} // original t -> new t (the first event at that instant)
	prevOld, prevNew := 0.0, 0.0
	for i, e := range c.Events {
		t, _ := num(e, "t")
		gap := t - prevOld
		switch {
		case i == 0:
			newT[i] = 0
		case gap == 0:
			newT[i] = prevNew
		case hold[i]:
			newT[i] = prevNew + gap
		default:
			newT[i] = prevNew + math.Max(1, math.Round(gap*between(0.7, 1.3)))
		}
		if _, seen := at[t]; !seen {
			at[t] = newT[i]
		}
		prevOld, prevNew = t, newT[i]
	}
	// mapTS puts an original Parallax time on the new timeline: the new time of the latest event at or before it,
	// plus the same offset, scaled.
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
		base := olds[j-1]
		return at[base] + math.Max(0, math.Round(ts-base))
	}

	socStart := between(20, 80)
	socScale := between(0.7, 1.3)
	kwScale := between(0.8, 1.2)
	mphScale := between(0.8, 1.2)
	noise := func(v, scale float64) float64 {
		if v == 0 {
			return 0
		}
		return math.Round(v*scale*between(0.95, 1.05)*10) / 10
	}
	var firstSoC, lastOldOdo, newOdo float64
	haveSoC, haveOdo := false, false
	for i, e := range c.Events {
		e["t"] = newT[i]
		if ts, ok := num(e, "ts"); ok {
			e["ts"] = mapTS(ts)
		}
		if v, ok := num(e, "soc"); ok && v > 0 {
			if !haveSoC {
				firstSoC, haveSoC = v, true
			}
			e["soc"] = math.Round((socStart+(v-firstSoC)*socScale)*10) / 10
		}
		if v, ok := num(e, "kw"); ok {
			e["kw"] = noise(v, kwScale)
		}
		if v, ok := num(e, "mph"); ok {
			e["mph"] = noise(v, mphScale)
		}
		if v, ok := num(e, "odo"); ok {
			if haveOdo {
				newOdo += math.Max(0, v-lastOldOdo) * mphScale
			}
			lastOldOdo, haveOdo = v, true
			e["odo"] = math.Round(newOdo*100) / 100
		}
		if !keepNotes {
			delete(e, "_note")
		}
	}
	return render(c.Vehicle, c.Events, seed)
}

// render writes the corpus in the committed layout: one event per line, keys in a fixed order.
var keyOrder = []string{"t", "kind", "rvm", "enum", "ts", "gear", "power", "mph", "odo", "cs", "status", "kw", "soc", "fields", "_note"}

func render(vehicle json.RawMessage, events []event, seed int64) ([]byte, error) {
	var b bytes.Buffer
	method := fmt.Sprintf("Perturbed from a real shape with cmd/corpusperturb (seed %d): gaps re-spaced, values regenerated, transitions kept; no real values.", seed)
	src, _ := json.Marshal("synthetic")
	m, _ := json.Marshal(method)
	fmt.Fprintf(&b, "{\n  \"_source\": %s,\n  \"_method\": %s,\n  \"vehicle\": %s,\n  \"events\": [\n", src, m, compact(vehicle))
	for i, e := range events {
		var parts []string
		seen := map[string]bool{}
		for _, k := range keyOrder {
			if v, ok := e[k]; ok {
				parts = append(parts, kv(k, v))
				seen[k] = true
			}
		}
		var rest []string
		for k := range e {
			if !seen[k] {
				rest = append(rest, k)
			}
		}
		sort.Strings(rest)
		for _, k := range rest {
			parts = append(parts, kv(k, e[k]))
		}
		sep := ","
		if i == len(events)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "    {%s}%s\n", strings.Join(parts, ", "), sep)
	}
	b.WriteString("  ]\n}\n")
	return b.Bytes(), nil
}

func kv(k string, v any) string {
	kb, _ := json.Marshal(k)
	vb, _ := json.Marshal(v)
	return string(kb) + ": " + string(vb)
}

func compact(r json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, r); err != nil {
		return string(r)
	}
	return strings.ReplaceAll(strings.ReplaceAll(b.String(), ":", ": "), ",", ", ")
}

