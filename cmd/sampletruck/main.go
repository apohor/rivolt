// Command sampletruck writes the generated sample-truck corpus (the
// demo's truck) as replay-format JSONL, one frame per
// line, and prints a summary to stderr. The same seed always produces
// byte-identical output.
//
//	go run ./cmd/sampletruck -o sample.jsonl
//	go run ./cmd/replay run sample.jsonl
//
// The server doesn't need the file: RIVIAN_CLIENT=replay with
// RIVOLT_REPLAY_SOURCE=sample generates the same frames on the fly.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/apohor/rivolt/internal/rivian"
)

func main() {
	seed := flag.Int64("seed", rivian.SampleSeed, "generator seed")
	out := flag.String("o", "-", "output file (- for stdout)")
	flag.Parse()

	c := rivian.NewSampleCorpus(*seed)
	var w io.Writer = os.Stdout
	if *out != "-" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "sampletruck:", err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	bw := bufio.NewWriter(w)
	if err := c.WriteJSONL(bw); err != nil {
		fmt.Fprintln(os.Stderr, "sampletruck:", err)
		os.Exit(1)
	}
	if err := bw.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "sampletruck:", err)
		os.Exit(1)
	}
	s := c.Summary()
	fmt.Fprintf(os.Stderr, "seed %d: %d frames, %s → %s\n", *seed, s.Frames, s.Start.Format("2006-01-02"), s.End.Format("2006-01-02"))
	fmt.Fprintf(os.Stderr, "  drives %d, home L2 plug-ins %d (+%d top-ups), DC fast %d, away AC %d, failed sleeps %d\n",
		s.Drives, s.HomeL2, s.TopUps, s.DCFC, s.AwayAC, s.FailedSleeps)
	fmt.Fprintf(os.Stderr, "  road trips %.0f mi, SoC %.0f–%.0f %%, odometer %.0f → %.0f mi\n",
		s.RoadTripMiles, s.MinSoC, s.MaxSoC, s.OdometerStartMi, s.OdometerEndMi)
}
