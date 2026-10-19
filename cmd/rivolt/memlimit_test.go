package main

import (
	"math"
	"runtime/debug"
	"testing"
)

func TestApplyMemoryLimit(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })

	t.Setenv("GOMEMLIMIT", "")
	t.Setenv("RIVOLT_MEMORY_LIMIT_BYTES", "536870912") // 512Mi
	if got := applyMemoryLimit(); got != 536870912/10*9 || debug.SetMemoryLimit(-1) != got {
		t.Errorf("512Mi limit: set %d (runtime %d), want 90%%", got, debug.SetMemoryLimit(-1))
	}

	debug.SetMemoryLimit(math.MaxInt64)
	for _, v := range []string{"", "0", "-5", "512Mi"} {
		t.Setenv("RIVOLT_MEMORY_LIMIT_BYTES", v)
		if got := applyMemoryLimit(); got != 0 || debug.SetMemoryLimit(-1) != math.MaxInt64 {
			t.Errorf("%q: set %d, want no change", v, got)
		}
	}

	t.Setenv("RIVOLT_MEMORY_LIMIT_BYTES", "536870912")
	t.Setenv("GOMEMLIMIT", "300MiB")
	if got := applyMemoryLimit(); got != 0 || debug.SetMemoryLimit(-1) != math.MaxInt64 {
		t.Errorf("explicit GOMEMLIMIT: set %d, want it left alone", got)
	}
}
