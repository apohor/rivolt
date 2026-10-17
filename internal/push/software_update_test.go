package push

import "testing"

func TestSoftwareUpdatePayload(t *testing.T) {
	p := softwareUpdatePayload("R1S", "2026.38", "https://rivian.com/notes/2026-38", false)
	if p.Title != "Software update available" || p.Body != "Update 2026.38 available for your R1S." ||
		p.URL != "https://rivian.com/notes/2026-38" || p.Kind != "software_update" {
		t.Errorf("available = %+v", p)
	}
	p = softwareUpdatePayload("R1T", "2026.38", "", true)
	if p.Title != "Software updated" || p.Body != "Your R1T is now on 2026.38." || p.URL != "/" {
		t.Errorf("installed = %+v", p)
	}
	// Only https links are followed; anything else opens the app.
	for _, bad := range []string{"javascript:alert(1)", "http://x.example/n", "//x.example", "/relative"} {
		if u := softwareUpdatePayload("R1S", "1", bad, false).URL; u != "/" {
			t.Errorf("notes url %q became %q, want /", bad, u)
		}
	}
	if p := softwareUpdatePayload("", "1", "", false); p.Body != "Update 1 available for your vehicle." {
		t.Errorf("no vehicle body = %q", p.Body)
	}
}

func TestSelectSoftwareUpdateHonoursThePreference(t *testing.T) {
	if !selectSoftwareUpdate(Subscription{OnSoftwareUpdate: true}) || selectSoftwareUpdate(Subscription{}) {
		t.Error("selector ignores OnSoftwareUpdate")
	}
}
