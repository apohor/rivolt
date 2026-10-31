package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/push"
)

// A user with no push store (no factory wired, or the store would not open) gets a clean 503 {"error":"unavailable"} from every /api/push
// route, through the real router; none of them dereferences the nil store.
func TestPushRoutesWithoutAStoreAnswer503(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{Auth: svc})
	h := asSession(router, "")
	n := 0
	err = chi.Walk(router.(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/push/") || route == "/api/push/vapid-key" {
			return nil
		}
		n++
		path := strings.ReplaceAll(route, "{id}", uuid.NewString())
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "https://rivolt.dev"+path, strings.NewReader("{}"))
		req.RemoteAddr = "203.0.113.9:4000"
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"unavailable"`) {
			t.Errorf("%s %s: %d %s, want 503 unavailable", method, route, rec.Code, rec.Body.String())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 10 {
		t.Fatalf("only %d push routes walked", n)
	}
}

// The handlers themselves are safe too, whoever mounts them.
func TestPushHandlersWithANilStoreAnswer503(t *testing.T) {
	var svc *push.Service
	for name, h := range map[string]http.HandlerFunc{
		"subscribe":     handlePushSubscribe(nil),
		"unsubscribe":   handlePushUnsubscribe(nil),
		"status":        handlePushStatus(svc, nil),
		"test":          handlePushTest(svc, nil),
		"device":        handlePushDevice(nil),
		"off":           handlePushOff(nil),
		"optin":         handlePushOptIn(nil),
		"apns register": handleAPNsRegister(svc, nil, nil),
		"apns prefs":    handleAPNsPreferences(svc, nil),
		"apns unreg":    handleAPNsUnregister(svc, nil),
		"notification":  handlePushNotification(nil),
		"apns test":     handleAPNsTest(svc, nil),
		"apns activity": handleAPNsActivity(svc, nil),
		"activity end":  handleAPNsActivityEnd(svc, nil),
		"activity test": handleAPNsActivityTest(svc, nil),
	} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/api/push/x", strings.NewReader("{}")))
		want := http.StatusServiceUnavailable
		if name == "subscribe" {
			want = http.StatusBadRequest // the body is validated before the store is needed; the router test below covers the 503
		}
		if rec.Code != want {
			t.Errorf("%s: %d %s, want %d", name, rec.Code, rec.Body.String(), want)
		}
	}
}
