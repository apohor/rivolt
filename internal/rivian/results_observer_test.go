package rivian

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type recordedResults struct {
	mu  sync.Mutex
	got []string
}

func (r *recordedResults) OnResult(op, class string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, op+"/"+class)
}

// Every call that reaches Rivian is counted by operation and class (the
// rivolt_rivian_results_total feed of the auth-failure alert); our own
// cancellation isn't Rivian's outcome and isn't counted.
func TestResultObserverCountsOutcomes(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = io.WriteString(w, `{"data":{"ok":true}}`)
			return
		}
		_, _ = io.WriteString(w, `{"errors":[{"message":"session expired"}]}`)
	}))
	defer server.Close()

	rec := &recordedResults{}
	c := NewLive().WithResultObserver(rec)
	c.endpoint = server.URL + "/gateway"
	call := func(ctx context.Context, op string) {
		_, _ = doGraphQLAt[map[string]any](ctx, c, c.endpoint, graphQLRequest{OperationName: op, Query: "query { ok }"}, nil)
	}

	call(context.Background(), "getVehicleState")
	status = http.StatusUnauthorized
	call(context.Background(), "getUserInfo")
	// A fresh client (the 401 above set needs-reauth, which would stop the
	// next call before the network anyway) with a cancelled context.
	status = http.StatusOK
	c = NewLive().WithResultObserver(rec)
	c.endpoint = server.URL + "/gateway"
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	call(cancelled, "getLiveSessionHistory")

	want := []string{"getVehicleState/success", "getUserInfo/user_action"}
	if len(rec.got) != len(want) {
		t.Fatalf("results = %v, want %v", rec.got, want)
	}
	for i := range want {
		if rec.got[i] != want[i] {
			t.Errorf("result %d = %q, want %q", i, rec.got[i], want[i])
		}
	}
}
