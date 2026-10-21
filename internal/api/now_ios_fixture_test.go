package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/samples"
)

// The iOS app decodes these server responses from fixtures in
// ios/RivoltTests/Fixtures (its unit tests run them through the real
// decoder). Decoding the same files strictly into the server's own types
// means a renamed or removed field the app reads fails here, in Go CI, before
// it reaches a phone. Adding a field doesn't: the app ignores unknown keys.
func TestIOSFixturesMatchServerTypes(t *testing.T) {
	for name, target := range map[string]any{
		"now_v1.json":         &nowResponse{},
		"now_v1_minimal.json": &nowResponse{},
		"state.json":          &rivian.State{},
		"vehicles.json":       &[]rivian.Vehicle{},
		"app_token.json":      &appTokenResponse{},
		// Slice B (the Home charts): the idle-awake bars, as the web's tile reads them.
		"sleep_activity.json": &[]samples.DayActivity{},
		// The Now card's detail (Sprint 7 slice A): the same calls as the web
		// Home page's LivePanel.
		"live_session.json":      &liveSessionResponse{},
		"live_session_home.json": &liveSessionResponse{},
		"live_drive.json":        &rivian.LiveDrive{},
		"drives_last.json":       &[]driveResponse{},
		"charges_last.json":      &[]chargeResponse{},
	} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("..", "..", "ios", "RivoltTests", "Fixtures", name))
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader(b))
			dec.DisallowUnknownFields()
			if err := dec.Decode(target); err != nil {
				t.Fatalf("fixture %s no longer matches the server type: %v", name, err)
			}
		})
	}
}
