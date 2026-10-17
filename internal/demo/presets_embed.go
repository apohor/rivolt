package demo

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
)

// Preset trips: the demo planner offers a small curated set instead of
// computing plans, so an anonymous visitor never makes the server call
// Rivian, a router, a geocoder or an LLM. The data is embedded:
//
//	trips/index.json        {"trips":[{id, name, inputs, summary}, ...]}
//	trips/<id>.plan.json    the plan response for that trip
//
// Both are checked in, sanitized (no user or Rivian identifiers).
//
//go:embed trips
var presetFS embed.FS

// PresetIDPattern is the shape of a preset id (also the file stem).
var presetIDPattern = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// PresetBlob is a static JSON document with its ETag.
type PresetBlob struct {
	Body []byte
	ETag string
}

type presetData struct {
	index PresetBlob
	plans map[string]PresetBlob
	ids   []string
	err   error
}

var (
	presetOnce sync.Once
	presets    presetData
)

func loadPresets() {
	presets = readPresets()
}

func blobOf(b []byte) PresetBlob {
	sum := sha256.Sum256(b)
	return PresetBlob{Body: b, ETag: `"` + hex.EncodeToString(sum[:8]) + `"`}
}

// readPresets reads and validates the embedded files. A malformed set
// is reported through err and served as empty, never partially.
func readPresets() presetData {
	empty := presetData{index: blobOf([]byte(`{"trips":[]}`)), plans: map[string]PresetBlob{}}
	raw, err := presetFS.ReadFile("trips/index.json")
	if err != nil {
		empty.err = err
		return empty
	}
	var idx struct {
		Trips []struct {
			ID string `json:"id"`
		} `json:"trips"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		empty.err = fmt.Errorf("demo presets: index.json: %w", err)
		return empty
	}
	out := presetData{plans: map[string]PresetBlob{}}
	for _, t := range idx.Trips {
		if !presetIDPattern.MatchString(t.ID) {
			empty.err = fmt.Errorf("demo presets: bad id %q", t.ID)
			return empty
		}
		if _, dup := out.plans[t.ID]; dup {
			empty.err = fmt.Errorf("demo presets: duplicate id %q", t.ID)
			return empty
		}
		plan, err := presetFS.ReadFile("trips/" + t.ID + ".plan.json")
		if err != nil {
			empty.err = fmt.Errorf("demo presets: plan for %q: %w", t.ID, err)
			return empty
		}
		if !json.Valid(plan) {
			empty.err = fmt.Errorf("demo presets: plan for %q is not JSON", t.ID)
			return empty
		}
		out.plans[t.ID] = blobOf(plan)
		out.ids = append(out.ids, t.ID)
	}
	out.index = blobOf(raw)
	return out
}

// PresetsIndex is the preset list document, `{"trips":[...]}`. Empty
// list when the data is absent or malformed (see PresetsErr).
func PresetsIndex() PresetBlob {
	presetOnce.Do(loadPresets)
	return presets.index
}

// PresetPlan returns the stored plan for a preset id.
func PresetPlan(id string) (PresetBlob, bool) {
	presetOnce.Do(loadPresets)
	b, ok := presets.plans[id]
	return b, ok
}

// PresetIDs lists the preset ids in index order.
func PresetIDs() []string {
	presetOnce.Do(loadPresets)
	return append([]string(nil), presets.ids...)
}

// PresetsErr reports why the embedded presets were rejected, if they were.
func PresetsErr() error {
	presetOnce.Do(loadPresets)
	return presets.err
}
