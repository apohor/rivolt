package main

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

// applyMemoryLimit sets the Go runtime's soft memory limit to 90% of the
// container's memory limit (RIVOLT_MEMORY_LIMIT_BYTES, which the Helm chart
// fills from the downward API), so the GC works harder before the kernel
// OOM-kills the pod. The rest is headroom for what the limit doesn't govern
// (stacks, runtime overhead). An explicit GOMEMLIMIT wins: the runtime has
// already applied it. It returns the limit it set, or 0.
func applyMemoryLimit() int64 {
	if os.Getenv("GOMEMLIMIT") != "" {
		return 0
	}
	v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("RIVOLT_MEMORY_LIMIT_BYTES")), 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	limit := v / 10 * 9
	debug.SetMemoryLimit(limit)
	return limit
}
