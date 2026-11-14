package push

import (
	"reflect"
	"strings"
	"testing"
)

func TestJoinClosures(t *testing.T) {
	for in, want := range map[string]string{
		"door":               "a door",
		"frunk":              "the frunk",
		"frunk,door":         "the frunk and a door",
		"door,frunk":         "the frunk and a door", // listed in a fixed order whatever the input
		"frunk,door,tonneau": "the frunk, a door and the tonneau",
		"liftgate,tailgate":  "the liftgate and the tailgate",
		"":                   "something",
		"window,sunroof":     "something", // not part of v1, never named
		"frunk,frunk":        "the frunk",
	} {
		var kinds []string
		if in != "" {
			kinds = strings.Split(in, ",")
		}
		if got := JoinClosures(kinds); got != want {
			t.Errorf("JoinClosures(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLeftOpenPayloadIsFixedTextWithNoPlaceAndNoPromise(t *testing.T) {
	p := LeftOpenPayload("R1S", []string{"frunk", "door"}, 15)
	if p.Title != "Left open" || p.Body != "The frunk and a door on your R1S have been open for 15 minutes." ||
		p.Kind != "left_open" || p.URL != "/" || p.Tag != "rivolt-left-open" {
		t.Errorf("payload = %+v", p)
	}
	// Several closures take "have", one takes "has", and the same kind twice is still one.
	for _, c := range []struct {
		kinds []string
		want  string
	}{
		{[]string{"door"}, "A door on your R1S has been open for 15 minutes."},
		{[]string{"door", "door"}, "A door on your R1S has been open for 15 minutes."},
		{[]string{"frunk", "door", "tonneau"}, "The frunk, a door and the tonneau on your R1S have been open for 15 minutes."},
		{[]string{"liftgate", "tailgate"}, "The liftgate and the tailgate on your R1S have been open for 15 minutes."},
		{nil, "Something on your R1S has been open for 15 minutes."},
	} {
		if got := LeftOpenPayload("R1S", c.kinds, 15).Body; got != c.want {
			t.Errorf("%v: %q, want %q", c.kinds, got, c.want)
		}
	}
	// The model only, never a nickname, and a blank model reads "your vehicle".
	if b := LeftOpenPayload("", []string{"door"}, 15).Body; b != "A door on your vehicle has been open for 15 minutes." {
		t.Errorf("no model: %q", b)
	}
	// The text promises nothing and never says "safe", "secure", "unsafe", "stolen" or names a place.
	for _, kinds := range [][]string{{"door"}, {"frunk", "door", "liftgate", "tailgate", "tonneau"}, nil} {
		b := strings.ToLower(LeftOpenPayload("R1T", kinds, 15).Body + " " + LeftOpenPayload("R1T", kinds, 15).Title)
		for _, bad := range []string{"safe", "secure", "unsafe", "stolen", "theft", "home", "location", "parked at"} {
			if strings.Contains(b, bad) {
				t.Errorf("%q contains %q", b, bad)
			}
		}
	}
}

func TestLeftOpenIsOptInPerDevice(t *testing.T) {
	if !selectLeftOpen(Subscription{OnLeftOpen: true}) || selectLeftOpen(Subscription{}) {
		t.Error("selector ignores OnLeftOpen")
	}
	if !apnsSelector("left_open", Preferences{OnLeftOpen: true}) || apnsSelector("left_open", Preferences{}) {
		t.Error("the app selector ignores OnLeftOpen")
	}
	// Nothing else turns it on.
	if apnsSelector("left_open", Preferences{OnChargingDone: true, OnAnomaly: true, OnSoftwareUpdate: true, OnPlugInReminder: true, OnMilestones: true}) {
		t.Error("another switch turned the left-open alert on")
	}
}

// Every kind the app's switches can select must be storable as an app notification, or the send is dropped after the switch said yes
// (left_open and tire_low were: notificationKinds did not list them).
func TestEveryAPNsSelectableKindCanBeStored(t *testing.T) {
	var all Preferences
	v := reflect.ValueOf(&all).Elem()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Kind() == reflect.Bool {
			v.Field(i).SetBool(true)
		}
	}
	kinds := []string{"charging_done", "plug_in_reminder", "anomaly", "software_update", "milestone", "left_open", "tire_low", "trip_recap"}
	for _, k := range kinds {
		if apnsSelector(k, all) && !notificationKinds[k] {
			t.Errorf("kind %q is routed to app devices but cannot be stored as a notification", k)
		}
	}
}
