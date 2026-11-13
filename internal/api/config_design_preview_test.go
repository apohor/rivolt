package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestParseDesignPreview(t *testing.T) {
	for in, want := range map[string][]string{
		"":                          {},
		" , ,":                      {},
		"theme_switch":              {"theme_switch"},
		"theme_switch, now_v2 ,":    {"theme_switch", "now_v2"},
		"now_v2,now_v2,unknown_key": {"now_v2", "unknown_key"},
	} {
		if got := ParseDesignPreview(in); !reflect.DeepEqual(got, want) {
			t.Errorf("ParseDesignPreview(%q) = %#v, want %#v", in, got, want)
		}
	}
}

// features.design_preview is always a JSON array (never null) and passes unknown keys through.
func TestConfigDesignPreviewFeature(t *testing.T) {
	for _, tc := range []struct {
		keys []string
		want string
	}{
		{nil, `[]`},
		{[]string{}, `[]`},
		{[]string{"theme_switch", "future_key"}, `["theme_switch","future_key"]`},
	} {
		h := handleConfig(false, false, false, false, nil, nil, configExtras{DesignPreview: tc.keys})
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		var body struct {
			Features struct {
				DesignPreview json.RawMessage `json:"design_preview"`
			} `json:"features"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if string(body.Features.DesignPreview) != tc.want {
			t.Fatalf("design_preview = %s, want %s", body.Features.DesignPreview, tc.want)
		}
	}
}
