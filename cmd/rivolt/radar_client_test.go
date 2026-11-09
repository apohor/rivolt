package main

import "testing"

func TestNewPlanRadar(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if r, err := newPlanRadar(env("")); r != nil || err != nil {
		t.Fatalf("empty means off: %v %v", r, err)
	}
	for _, bad := range []string{"radar.radar.svc", "ftp://x", "http://u:p@x", "http://x?a=1", "http://"} {
		if _, err := newPlanRadar(env(bad)); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if r, err := newPlanRadar(env("http://radar.radar.svc/")); r == nil || err != nil {
		t.Fatalf("valid URL: %v %v", r, err)
	}
}
