package adminaudit

import (
	"reflect"
	"testing"
)

// Details keep only booleans, numbers and short enum tokens; free text,
// secrets, emails and long values never get stored.
func TestCleanKeepsOnlySafeValues(t *testing.T) {
	in := map[string]any{
		"role":         "admin",
		"disabled":     true,
		"limit":        float64(100),
		"daily_limit":  3,
		"api_key":      "sk-ant-api03-SECRET-SECRET-SECRET",   // not an enum token (uppercase, too long)
		"reason":       "Rivian is rate limiting us, see ops", // free text
		"email":        "someone@example.com",
		"Bad Key":      "x", // key itself not a token
		"nested":       map[string]any{"a": "b"},
		"users":        []any{"u1"}, // []any is not stored
		"fields":       []string{"provider", "openai_api_key", "Has Space", "OK_NOT"},
		"long":         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"upper":        "ADMIN",
		"enum.with:ok": "a-b_c.d:e",
	}
	got := Clean(in)
	want := map[string]any{
		"role": "admin", "disabled": true, "limit": float64(100), "daily_limit": 3,
		"fields": []string{"provider", "openai_api_key"}, "enum.with:ok": "a-b_c.d:e",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Clean = %#v\nwant   %#v", got, want)
	}
}

func TestCleanCapsTheNumberOfKeys(t *testing.T) {
	in := map[string]any{}
	for i := 0; i < 50; i++ {
		in["k"+string(rune('a'+i%26))+string(rune('a'+i/26))] = i
	}
	if n := len(Clean(in)); n != 20 {
		t.Errorf("kept %d keys, want 20", n)
	}
}
