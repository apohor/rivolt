package settings

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// User-defined trip-planner favorite destinations. Surfaced as
// preset buttons under both the Origin and Destination fields on
// the trip-planner page so a one-tap pick replaces the previous
// regional hardcoded list.
//
// Stored as a single JSON-encoded value under one settings key
// rather than one row per favorite. The list is small (cap at
// FAVORITES_MAX) and only ever read/written wholesale, so the
// blob shape is simpler than maintaining a per-favorite KV row
// schema.
const keyPlannerFavorites = "planner.favorites"

// FavoritesMax caps how many favorites we'll persist. Plenty for a
// human roster, keeps the JSON blob bounded.
const FavoritesMax = 25

// LabelMaxLen caps the human label so a rogue paste doesn't blow
// up the UI chip. Address-shape strings often go ~120 chars; we
// trim hard so chips stay compact.
const LabelMaxLen = 80

// PlannerFavorite is one saved place — coordinates plus a short
// user-visible name. ID is a UUID minted client-side so deletes
// and renames don't need a separate lookup.
type PlannerFavorite struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// GetPlannerFavorites returns the user's saved favorites. Returns
// nil (not an error) when nothing has been saved or the stored
// blob fails to parse — a corrupt blob is treated the same as
// "not configured" so a single bad write doesn't permanently
// break the planner page.
func GetPlannerFavorites(ctx context.Context, s *Store) ([]PlannerFavorite, error) {
	if s == nil {
		return nil, nil
	}
	all, err := s.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	raw, ok := all[keyPlannerFavorites]
	if !ok || raw == "" {
		return nil, nil
	}
	var list []PlannerFavorite
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		// Corrupt blob — treat as empty. Caller can overwrite.
		return nil, nil
	}
	return list, nil
}

// SetPlannerFavorites persists the favorites list. Validates each
// entry's shape (non-zero coords, non-empty label) and caps the
// total at FavoritesMax — invalid entries are silently dropped to
// keep the persisted blob clean.
func SetPlannerFavorites(ctx context.Context, s *Store, list []PlannerFavorite) error {
	if s == nil {
		return nil
	}
	cleaned := make([]PlannerFavorite, 0, len(list))
	for _, f := range list {
		label := strings.TrimSpace(f.Label)
		if label == "" {
			continue
		}
		if len(label) > LabelMaxLen {
			label = label[:LabelMaxLen]
		}
		if f.Latitude == 0 && f.Longitude == 0 {
			continue
		}
		if f.ID == "" {
			// Client should mint one; reject anonymous entries so
			// later edits can target a stable id.
			continue
		}
		cleaned = append(cleaned, PlannerFavorite{
			ID:        f.ID,
			Label:     label,
			Latitude:  f.Latitude,
			Longitude: f.Longitude,
		})
		if len(cleaned) >= FavoritesMax {
			break
		}
	}
	buf, err := json.Marshal(cleaned)
	if err != nil {
		return fmt.Errorf("planner favorites marshal: %w", err)
	}
	return s.Set(ctx, keyPlannerFavorites, string(buf))
}
