package privacy

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The iPhone app never uses location services: maps show a recorded route or place, never where the phone is.
// No location manager, no permission request, no location purpose string, no user-location dot.
func TestIOSUsesNoLocationServices(t *testing.T) {
	forbidden := []string{
		"CLLocationManager", "requestWhenInUseAuthorization", "requestAlwaysAuthorization",
		"NSLocationWhenInUseUsageDescription", "NSLocationAlwaysAndWhenInUseUsageDescription",
		"NSLocationAlwaysUsageDescription", "showsUserLocation = true", "showsUserLocation: true",
	}
	root := filepath.Join("..", "..", "ios")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || strings.HasSuffix(d.Name(), ".xcodeproj") {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".swift", ".plist", ".yml", ".entitlements":
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, f := range forbidden {
			if strings.Contains(string(b), f) {
				t.Errorf("%s uses %s: the app has no location services", path, f)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The bundled map styles name nothing on the network but the signed-in server's tile archive; everything else is in
// the app bundle (asset://). No third-party style, tile, glyph or sprite host.
func TestIOSMapStylesNameOnlyTheServerAndTheBundle(t *testing.T) {
	urlish := regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)
	files, err := filepath.Glob(filepath.Join("..", "..", "ios", "Rivolt", "Resources", "Map", "style-*.json"))
	if err != nil || len(files) != 2 {
		t.Fatalf("want the light and dark styles, got %v (%v)", files, err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var style any
		if err := json.Unmarshal(b, &style); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		var walk func(v any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				for _, c := range x {
					walk(c)
				}
			case []any:
				for _, c := range x {
					walk(c)
				}
			case string:
				if !urlish.MatchString(x) {
					return
				}
				if !strings.HasPrefix(x, "asset://") && x != "pmtiles://__RIVOLT_SERVER__/api/maps/tiles/us.pmtiles" {
					t.Errorf("%s names %q: only asset:// and the server's tile archive are allowed", filepath.Base(f), x)
				}
			}
		}
		walk(style)
		m := style.(map[string]any)
		if _, ok := m["sprite"]; ok {
			t.Errorf("%s has a sprite: none is bundled yet", filepath.Base(f))
		}
	}
}
