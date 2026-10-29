package api

import (
	"strings"
	"testing"
)

// knownLong are older descriptions over the limit, DA-approved as worded; changing one is a copy change for growth and DA. A new page
// does not get to join this list.
var knownLong = map[string]bool{
	"rivolt.dev/lease-calculator": true,
	"rivolt.dev/safe-to-connect":  true,
	"demo.rivolt.dev/":            true,
}

// Search engines cut a title at about 60 characters and a description at about 155; every public page stays within a small margin of both.
func TestSEOTitlesAndDescriptionsFitTheirLimits(t *testing.T) {
	for host, pages := range seoPages {
		for path, p := range pages {
			if n := len([]rune(p.title)); n > 70 {
				t.Errorf("%s%s: title is %d characters (limit 70): %q", host, path, n, p.title)
			}
			if n := len([]rune(p.desc)); n > 160 && !knownLong[host+path] {
				t.Errorf("%s%s: description is %d characters (limit 160): %s", host, path, n, strings.TrimSpace(p.desc))
			}
		}
	}
}
