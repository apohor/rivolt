// Command replay runs a captured frame stream (JSONL) through the recorder
// offline and prints the drives it produces — a hand-debugging companion to
// the replay-harness unit tests. See docs/REPLAY_HARNESS.md for the JSONL
// format and the psql export query that generates fixtures.
package main

import (
	"fmt"
	"os"

	"github.com/apohor/rivolt/internal/rivian"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "run" {
		fmt.Fprintln(os.Stderr, "usage: replay run <fixture.jsonl>")
		os.Exit(2)
	}
	f, err := os.Open(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()

	frames, err := rivian.FramesFromJSONL(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse:", err)
		os.Exit(1)
	}
	r := rivian.NewReplayer("replay")
	r.FeedAll(frames)
	drives := r.Drives()

	fmt.Printf("%d frames -> %d drive(s)\n", len(frames), len(drives))
	for i, d := range drives {
		fmt.Printf("  %d: %.1f mi  %s -> %s  SoC %.0f->%.0f\n",
			i+1, d.DistanceMi,
			d.StartedAt.Format("2006-01-02 15:04:05"),
			d.EndedAt.Format("15:04:05"),
			d.StartSoCPct, d.EndSoCPct)
	}
}
