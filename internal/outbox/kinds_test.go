package outbox

import "testing"

// A kind with no MaxAge would never expire its pending rows (the purge cutoff is NULL), so every kind needs one.
func TestEveryKindHasMaxAge(t *testing.T) {
	for k := range kinds {
		if MaxAge[k] <= 0 {
			t.Errorf("kind %s has no MaxAge", k)
		}
	}
	for k := range MaxAge {
		if _, ok := kinds[k]; !ok {
			t.Errorf("MaxAge lists unknown kind %s", k)
		}
	}
}
