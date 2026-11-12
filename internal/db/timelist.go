package db

import (
	"strings"
	"time"
)

// TimeList renders times as one comma-separated RFC 3339 string, for string_to_array($n, ',')::timestamptz[] in a query that takes a
// list of instants as one parameter.
func TimeList(ts []time.Time) string {
	var b strings.Builder
	for i, t := range ts {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(t.UTC().Format(time.RFC3339Nano))
	}
	return b.String()
}
