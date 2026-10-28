package main

import "testing"

func TestReplicaDivisorFromEnv(t *testing.T) {
	get := func(v string) func(string) string { return func(string) string { return v } }
	for in, want := range map[string]float64{"": 3, "2": 2, " 5 ": 5, "100": 100, "0": 3, "101": 3, "-1": 3, "x": 3} {
		if got := replicaDivisorFromEnv(get(in)); got != want {
			t.Errorf("RIVOLT_REPLICA_DIVISOR=%q -> %v, want %v", in, got, want)
		}
	}
}
