package tripadvice

import (
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/weather"
)

func TestPromptLabelsWindAgainstTheFirstLeg(t *testing.T) {
	plan := &rivian.TripPlan{Status: "OK", Routes: []rivian.TripRoute{{DestinationReached: true}}}
	for _, tc := range []struct {
		hw   float64
		want string
	}{
		{-30, "(30 kph tailwind on the first leg)"},
		{20, "(20 kph headwind on the first leg)"},
		{1, "(crosswind on the first leg)"},
	} {
		snap := &weather.Snapshot{WindKPH: 30, HasWind: true, WindDirDeg: 0, HeadwindKPH: tc.hw, HasHeadwind: true}
		_, prompt := buildPrompt(plan, Context{StartingSoC: 90, Weather: snap}, CostEstimate{})
		if !strings.Contains(prompt, "Wind: 30 kph from 0° "+tc.want) {
			t.Errorf("hw %v: want %q in:\n%s", tc.hw, tc.want, prompt)
		}
	}
}
