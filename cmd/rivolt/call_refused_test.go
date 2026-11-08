package main

import (
	"reflect"
	"testing"

	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
)

// The monitor's call sites and the counter's label values are one list, and every site's series exists at 0 from boot so the first refusal
// is an increase.
func TestCallRefusedSitesMatchTheMetric(t *testing.T) {
	if !reflect.DeepEqual(rivian.CallSites, metrics.RivianCallSites) {
		t.Fatalf("rivian.CallSites %v differs from metrics.RivianCallSites %v", rivian.CallSites, metrics.RivianCallSites)
	}
	m := metrics.New()
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "rivolt_rivian_call_refused_total" {
			continue
		}
		if got := len(mf.GetMetric()); got != len(rivian.CallSites) {
			t.Errorf("%d series, want one per site (%d), all at 0", got, len(rivian.CallSites))
		}
		for _, s := range mf.GetMetric() {
			if s.GetCounter().GetValue() != 0 {
				t.Errorf("a series starts non-zero: %v", s)
			}
		}
		return
	}
	t.Fatal("rivolt_rivian_call_refused_total is not registered")
}

// The permit's counter exists at 0 from boot, so the first unpermitted request is an increase.
func TestUnpermittedCounterStartsAtZero(t *testing.T) {
	mfs, err := metrics.New().Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() == "rivolt_rivian_unpermitted_total" {
			if len(mf.GetMetric()) != 1 || mf.GetMetric()[0].GetCounter().GetValue() != 0 {
				t.Errorf("unexpected series: %v", mf.GetMetric())
			}
			return
		}
	}
	t.Fatal("rivolt_rivian_unpermitted_total is not registered")
}
