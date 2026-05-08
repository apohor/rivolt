package drives

import "testing"

func TestIsReparking(t *testing.T) {
	tests := []struct {
		name string
		d    Drive
		want bool
	}{
		{"zero distance, no SoC delta", Drive{DistanceMi: 0, StartSoCPct: 52.1, EndSoCPct: 52.1}, true},
		{"tiny distance, no SoC delta", Drive{DistanceMi: 0.02, StartSoCPct: 52.1, EndSoCPct: 52.1}, true},
		{"at threshold (0.05 mi), no SoC delta — kept", Drive{DistanceMi: 0.05, StartSoCPct: 52.1, EndSoCPct: 52.1}, false},
		{"real short errand", Drive{DistanceMi: 0.3, StartSoCPct: 52.1, EndSoCPct: 52.1}, false},
		{"sub-threshold but SoC dropped — kept (real consumption)", Drive{DistanceMi: 0.02, StartSoCPct: 52.1, EndSoCPct: 51.9}, false},
		{"sub-threshold but SoC rose — kept (regen / charge mid-drive)", Drive{DistanceMi: 0.02, StartSoCPct: 51.9, EndSoCPct: 52.1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsReparking(tt.d); got != tt.want {
				t.Errorf("IsReparking = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterReparking(t *testing.T) {
	in := []Drive{
		{DistanceMi: 2.5, StartSoCPct: 53, EndSoCPct: 52},
		{DistanceMi: 0, StartSoCPct: 52, EndSoCPct: 52},
		{DistanceMi: 0.02, StartSoCPct: 52, EndSoCPct: 52},
		{DistanceMi: 1.0, StartSoCPct: 52, EndSoCPct: 51},
	}
	out := FilterReparking(in)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0].DistanceMi != 2.5 || out[1].DistanceMi != 1.0 {
		t.Errorf("wrong drives kept: %+v", out)
	}
}
