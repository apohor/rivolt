package demo

import "github.com/apohor/rivolt/internal/offpeak"

// DemoOffpeakRate is the example overnight rate of the demo's schedule, USD per
// kWh, against HomeRate for every other hour. An example, not any utility's tariff.
const DemoOffpeakRate = 0.09

// DemoSchedule is the demo owner's example off-peak schedule: weekday nights from
// 21:00 to 06:00 in the home's time zone (Pasadena, Pacific time). It is what the
// public demo's savings card is computed from, shown as "Sample truck · example
// schedule". Normalized (the same checks as a user's schedule); the panic is for a
// programming error caught by the tests.
func DemoSchedule() offpeak.Schedule {
	s, e := offpeak.Normalize(offpeak.Schedule{
		TZ: "America/Los_Angeles",
		Windows: []offpeak.Window{
			{Days: offpeak.Weekdays, Start: "21:00", End: "06:00", Rate: DemoOffpeakRate, Label: "Overnight (example)"},
		},
	})
	if e != nil {
		panic("demo: example schedule: " + e.Error())
	}
	return s
}
