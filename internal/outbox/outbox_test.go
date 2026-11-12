package outbox

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestIntentValidation(t *testing.T) {
	u, v := uuid.New(), uuid.New()
	for name, c := range map[string]struct {
		in   Intent
		want string // substring of the error; "" is valid
	}{
		"vehicle kind":          {Intent{Key: "k1", UserID: u, VehicleID: v, Kind: KindCloseHook}, ""},
		"account kind":          {Intent{Key: "welcome:" + u.String(), UserID: u, Kind: KindEmail}, ""},
		"unknown kind":          {Intent{Key: "k", UserID: u, VehicleID: v, Kind: "sms"}, "unknown kind"},
		"empty key":             {Intent{UserID: u, VehicleID: v, Kind: KindPush}, "key must be"},
		"long key":              {Intent{Key: strings.Repeat("k", 201), UserID: u, VehicleID: v, Kind: KindPush}, "key must be"},
		"no user":               {Intent{Key: "k", VehicleID: v, Kind: KindPush}, "user id"},
		"vehicle kind no truck": {Intent{Key: "k", UserID: u, Kind: KindPush}, "needs a vehicle"},
		"account kind w/truck":  {Intent{Key: "k" + u.String(), UserID: u, VehicleID: v, Kind: KindEmail}, "takes no vehicle"},
		"account key no user":   {Intent{Key: "welcome", UserID: u, Kind: KindEmail}, "carry the user id"},
		"payload too big":       {Intent{Key: "k", UserID: u, VehicleID: v, Kind: KindPush, Payload: []byte(`"` + strings.Repeat("x", MaxPayload) + `"`)}, "payload over"},
		"payload not json":      {Intent{Key: "k", UserID: u, VehicleID: v, Kind: KindPush, Payload: []byte(`{`)}, "not JSON"},
	} {
		err := c.in.validate()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err %v, want %q", name, err, c.want)
		}
	}
}

// Every kind has a max age, so no kind can be sent arbitrarily late.
func TestEveryKindHasAMaxAge(t *testing.T) {
	for k := range kinds {
		if MaxAge[k] <= 0 {
			t.Errorf("kind %s has no MaxAge", k)
		}
	}
}
