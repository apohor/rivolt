package api

import "testing"

func TestTrendMedian(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		in   []float64
		want float64
	}{
		"empty": {nil, 0}, "one": {[]float64{5}, 5}, "odd": {[]float64{9, 1, 5}, 5},
		"even": {[]float64{4, 1, 3, 2}, 2.5}, "unsorted with repeats": {[]float64{3, 3, 1, 9, 3}, 3},
	} {
		if got := median(c.in); got != c.want {
			t.Errorf("%s: median(%v) = %v, want %v", name, c.in, got, c.want)
		}
	}
}
