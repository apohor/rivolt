package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/firstscreen"
	"github.com/apohor/rivolt/internal/sessions"
)

type fakeFirstScreen struct {
	calls    int
	receipts []bool
}

func (f *fakeFirstScreen) FirstScreen(_ context.Context, _ uuid.UUID, receipts bool) (firstscreen.Data, error) {
	f.calls++
	f.receipts = append(f.receipts, receipts)
	return firstscreen.Data{Vehicles: 1}, nil
}

func firstScreenGet(t *testing.T, h http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev/api/first-screen", nil)
	req.RemoteAddr = "203.0.113.9:4000"
	h.ServeHTTP(rec, req)
	return rec
}

// With RIVOLT_FIRST_SCREEN off there is no source and no route: 404, for a signed-in user too.
func TestFirstScreenIsAbsentWhenOff(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	if rec := firstScreenGet(t, asSession(New(Deps{Auth: svc}), sessions.KindApp)); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

// On: the route answers through the real router, makes no Rivian call (a counting client sits in Deps), reads no receipts for a user who
// has not opted in, and answers 429 once the user's 12 a minute are spent.
func TestFirstScreenMakesNoRivianCallsAndIsLimited(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	client := &restStateClient{}
	src := &fakeFirstScreen{}
	h := asSession(New(Deps{Auth: svc, Rivian: client, FirstScreen: src}), sessions.KindApp)
	answered, limited := 0, 0
	for i := 0; i < 2*firstScreenPerMin; i++ {
		rec := firstScreenGet(t, h)
		switch rec.Code {
		case http.StatusOK:
			answered++
			if !strings.Contains(rec.Body.String(), `"vehicles":1`) || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("body %s headers %v", rec.Body.String(), rec.Header())
			}
		case http.StatusTooManyRequests:
			limited++
		default:
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
	}
	if answered < firstScreenPerMin || answered > firstScreenPerMin+2 || limited == 0 {
		t.Errorf("%d answered and %d limited of %d, want about %d answered then limited", answered, limited, 2*firstScreenPerMin, firstScreenPerMin)
	}
	if client.count() != 0 {
		t.Errorf("the first screen called Rivian %d times, want 0", client.count())
	}
	for _, r := range src.receipts {
		if r {
			t.Error("receipts were read for a user who never opted in")
		}
	}
}
