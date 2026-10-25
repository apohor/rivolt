package db

import "testing"

func TestRequestPoolMaxConns(t *testing.T) {
	for v, want := range map[string]int32{"": DefaultRequestMaxConns, "x": DefaultRequestMaxConns, "0": DefaultRequestMaxConns, "-3": DefaultRequestMaxConns, "101": DefaultRequestMaxConns, "1": 1, " 25 ": 25, "100": 100} {
		if got := RequestPoolMaxConns(func(string) string { return v }); got != want {
			t.Errorf("RIVOLT_REQUEST_DB_MAX_CONNS=%q: %d, want %d", v, got, want)
		}
	}
	if DefaultRequestMaxConns < 1 {
		t.Fatal("default out of range")
	}
}

// DA's condition on S2-1: per pod times replicas stays within 45 of the role's 50. Prod, preview and demo run 2 replicas with a
// rolling update that surges 1 (deploy/helm/rivolt values: maxSurge 1), so 3 pods at the default.
func TestRequestPoolDefaultFitsTheRoleBudget(t *testing.T) {
	const replicas, surge = 2, 1
	if got := int(DefaultRequestMaxConns) * (replicas + surge); got > RequestConnBudget {
		t.Errorf("%d pods x %d = %d request connections, over the budget of %d", replicas+surge, DefaultRequestMaxConns, got, RequestConnBudget)
	}
}
