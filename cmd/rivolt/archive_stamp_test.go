package main

import (
	"os"
	"regexp"
	"testing"
)

// The archive deletion job runs only where the archive_deletions starter does (the jobs role). Its last-run stamp is set inside that starter, so
// a pod of another role exports 0 and an alert reading "newest run across pods" is not fooled by a boot-time stamp on an api pod.
func TestArchiveDeletionStampIsSetOnlyWhereTheJobRuns(t *testing.T) {
	b, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	inStarter := regexp.MustCompile(`(?s)role\.start\("archive_deletions", func\(\) \{\s*appMetrics\.ArchiveDeletionsLastRun\.SetToCurrentTime\(\)`)
	if !inStarter.MatchString(s) {
		t.Error("the archive_deletions starter must set ArchiveDeletionsLastRun itself, as its first statement")
	}
	// The other use is the Report callback, which only the running job calls.
	if n := len(regexp.MustCompile(`ArchiveDeletionsLastRun\.SetToCurrentTime\(\)`).FindAllString(s, -1)); n != 2 {
		t.Errorf("ArchiveDeletionsLastRun is stamped %d times, want 2 (the starter's boot and the job's Report)", n)
	}
}
