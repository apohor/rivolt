package api

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/ledger"
)

func sampleExport() exportData {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	km := 3.2
	th := 1.5
	return exportData{
		Version: "v0.32.1", CreatedAt: now, TZ: "UTC", Currency: "USD",
		Charges: []charges.Charge{{
			ID: "live_v_c_1", VehicleID: "rv-1", StartedAt: now.Add(-3 * time.Hour), EndedAt: now.Add(-2 * time.Hour),
			EnergyAddedKWh: 30.5, Cost: 4.88, Currency: "USD", PricePerKWh: 0.16, CostSource: "home_rate",
			Network: "=HYPERLINK(\"http://x\")", Place: "home", ThermalKWh: &th, Source: "live",
		}},
		Classes: []ledger.Classification{{CostSource: "home_rate", Kind: "ac", Place: "home", KMFromHome: &km, CountsInSpend: true, Flags: []string{"a", "b"}}},
		Drives: []drives.Drive{{ID: "d1", VehicleID: "rv-1", StartedAt: now.Add(-5 * time.Hour), EndedAt: now.Add(-4 * time.Hour),
			DistanceMi: 12.5, StartLat: 34.1, StartLon: -118.1, Source: "live", RoutePolyline: "abc"}},
		Trips: []ledger.Trip{{TripID: "trip_1", VehicleID: "rv-1", StartedAt: now.Add(-48 * time.Hour), EndedAt: now.Add(-24 * time.Hour),
			Miles: 600, DriveIDs: []string{"d1", "d2"}, ChargeIDs: []string{"c1"}, CostSourceMix: map[string]float64{"network_estimate": 30, "home_rate": 5}}},
		Settings: map[string]string{"charging.home_price_per_kwh": "0.16"},
		Profiles: []exportProfile{},
	}
}

func readZip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(data)
	}
	return out
}

func TestExportZip_ContentsAndEffectiveColumns(t *testing.T) {
	var buf bytes.Buffer
	if err := writeExportZip(&buf, sampleExport()); err != nil {
		t.Fatal(err)
	}
	files := readZip(t, buf.Bytes())
	for _, name := range []string{"README.txt", "charges.csv", "drives.csv", "trips.csv", "settings.json", "vehicles.json", "export.json"} {
		if _, ok := files[name]; !ok {
			t.Errorf("missing %s", name)
		}
	}
	rows, err := csv.NewReader(strings.NewReader(files["charges.csv"])).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("charges.csv: %v rows=%d", err, len(rows))
	}
	col := map[string]string{}
	for i, h := range rows[0] {
		col[h] = rows[1][i]
	}
	if col["cost_source"] != "home_rate" || col["effective_cost_source"] != "home_rate" || col["kind"] != "ac" ||
		col["counts_in_spend"] != "true" || col["flags"] != "a;b" || col["km_from_home"] != "3.2" {
		t.Fatalf("charge row: %v", col)
	}
	if !strings.HasPrefix(col["network"], "'=") {
		t.Fatalf("formula injection not neutralised: %q", col["network"])
	}
	var meta struct {
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal([]byte(files["export.json"]), &meta); err != nil || meta.Counts["charges"] != 1 || meta.Counts["trips"] != 1 {
		t.Fatalf("export.json: %v %s", err, files["export.json"])
	}
	if !strings.Contains(files["trips.csv"], "home_rate=5;network_estimate=30") {
		t.Fatalf("trips.csv mix: %s", files["trips.csv"])
	}
}

func TestExportSettings_AllowlistOnly(t *testing.T) {
	got := exportSettings(map[string]string{
		"charging.home_price_per_kwh": "0.16",
		"home_location.latitude":      "34.1",
		"ai.openai.api_key":           "sk-secret",
		"rivian.session_token":        "t",
		"webhook.url":                 "https://hooks.example.com/x",
		"some.future.key":             "v",
	})
	if len(got) != 2 || got["charging.home_price_per_kwh"] != "0.16" || got["home_location.latitude"] != "34.1" {
		t.Fatalf("settings = %v", got)
	}
}

// Every key literal in internal/settings must be classified: exported
// or deliberately not. A new key fails here until someone decides.
func TestExportSettingKeysAreClassified(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, "../settings", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	keyRe := regexp.MustCompile(`^[a-z_]+(\.[a-z_0-9]+)+$`)
	seen := 0
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil || !keyRe.MatchString(v) {
					return true
				}
				seen++
				if !exportedSettingKeys[v] && !notExportedSettingKeys[v] {
					t.Errorf("settings key %q is not classified in export.go (exportedSettingKeys / notExportedSettingKeys)", v)
				}
				return true
			})
		}
	}
	if seen < 20 {
		t.Fatalf("only found %d key literals; the scan is broken", seen)
	}
	for k := range exportedSettingKeys {
		if notExportedSettingKeys[k] {
			t.Errorf("%q is in both lists", k)
		}
	}
}

func TestExport_CSVQuotesEveryStringCell(t *testing.T) {
	e := sampleExport()
	e.Charges[0].Currency = "@usd"
	e.Charges[0].Place = "=home"
	e.Drives[0].RoutePolyline = "@abc"
	e.Drives[0].ID = "+d1"
	e.Trips[0].TripID = "-trip"
	var buf bytes.Buffer
	if err := writeExportZip(&buf, e); err != nil {
		t.Fatal(err)
	}
	files := readZip(t, buf.Bytes())
	cell := func(file, col string) string {
		rows, err := csv.NewReader(strings.NewReader(files[file])).ReadAll()
		if err != nil || len(rows) < 2 {
			t.Fatalf("%s: %v", file, err)
		}
		for i, h := range rows[0] {
			if h == col {
				return rows[1][i]
			}
		}
		t.Fatalf("%s has no column %s", file, col)
		return ""
	}
	for _, c := range []struct{ file, col, want string }{
		{"charges.csv", "currency", "'@usd"},
		{"charges.csv", "place", "'=home"},
		{"drives.csv", "route_polyline", "'@abc"},
		{"drives.csv", "id", "'+d1"},
		{"trips.csv", "trip_id", "'-trip"},
	} {
		if got := cell(c.file, c.col); got != c.want {
			t.Errorf("%s %s = %q, want %q", c.file, c.col, got, c.want)
		}
	}
	// Numbers are not text: a negative coordinate stays a number.
	e.Drives[0].StartLon = -118.1
	buf.Reset()
	if err := writeExportZip(&buf, e); err != nil {
		t.Fatal(err)
	}
	files = readZip(t, buf.Bytes())
	if got := cell("drives.csv", "start_lon"); got != "-118.1" {
		t.Errorf("start_lon = %q, want an unquoted number", got)
	}
}

func TestExportHandler_ForbiddenWhileImpersonating(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/export", nil)
	ctx := auth.WithImpersonation(req.Context(), uuid.New(), uuid.New())
	ctx = auth.WithUser(ctx, uuid.New())
	rec := httptest.NewRecorder()
	handleExport(Deps{})(rec, req.WithContext(ctx))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("impersonated export = %d, want 403", rec.Code)
	}
}

func TestExportJSON_SingleDocument(t *testing.T) {
	var buf bytes.Buffer
	if err := writeExportJSON(&buf, sampleExport()); err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"export", "charges", "drives", "trips", "settings", "vehicle_profiles"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("missing %q", k)
		}
	}
	if strings.Contains(buf.String(), "sk-secret") {
		t.Fatal("secret leaked")
	}
}

func TestCSVText(t *testing.T) {
	for in, want := range map[string]string{"": "", "plain": "plain", "=1+1": "'=1+1", "+x": "'+x", "-x": "'-x", "@x": "'@x", "\tx": "'\tx"} {
		if got := csvText(in); got != want {
			t.Errorf("csvText(%q) = %q, want %q", in, got, want)
		}
	}
}
