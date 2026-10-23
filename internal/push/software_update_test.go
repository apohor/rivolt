package push

import "testing"

func TestSoftwareUpdatePayload(t *testing.T) {
	p := softwareUpdatePayload("R1S", "2026.38", false)
	// The tap opens the app: Rivian's release-notes link is a one-hour signed URL, never put in a payload.
	if p.Title != "Software update available" || p.Body != "Update 2026.38 available for your R1S." ||
		p.URL != "/" || p.Kind != "software_update" {
		t.Errorf("available = %+v", p)
	}
	p = softwareUpdatePayload("R1T", "2026.38", true)
	if p.Title != "Software updated" || p.Body != "Your R1T is now on 2026.38." || p.URL != "/" {
		t.Errorf("installed = %+v", p)
	}
	if p := softwareUpdatePayload("", "1", false); p.Body != "Update 1 available for your vehicle." {
		t.Errorf("no vehicle body = %q", p.Body)
	}
}

func TestSelectSoftwareUpdateHonoursThePreference(t *testing.T) {
	if !selectSoftwareUpdate(Subscription{OnSoftwareUpdate: true}) || selectSoftwareUpdate(Subscription{}) {
		t.Error("selector ignores OnSoftwareUpdate")
	}
}

func TestPlugInReminderPayload(t *testing.T) {
	p := PlugInReminderPayload(32, 40)
	if p.Title != "Plug in tonight?" || p.Body != "Battery at 32%. Below your 40% reminder level." || p.Kind != "plug_in_reminder" || p.URL != "/" {
		t.Errorf("payload %+v", p)
	}
}
