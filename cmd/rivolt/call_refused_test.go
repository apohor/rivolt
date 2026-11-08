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

// The permit's counter exists at 0 for every operation from boot, so the first unpermitted request is an increase; the label list is the
// client's.
func TestUnpermittedCounterStartsAtZeroPerOp(t *testing.T) {
	if !reflect.DeepEqual(rivian.PermitOps, metrics.RivianPermitOps) {
		t.Fatalf("rivian.PermitOps %v differs from metrics.RivianPermitOps %v", rivian.PermitOps, metrics.RivianPermitOps)
	}
	mfs, err := metrics.New().Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "rivolt_rivian_unpermitted_total" {
			continue
		}
		if len(mf.GetMetric()) != len(rivian.PermitOps) {
			t.Errorf("%d series, want one per operation (%d)", len(mf.GetMetric()), len(rivian.PermitOps))
		}
		for _, s := range mf.GetMetric() {
			if s.GetCounter().GetValue() != 0 {
				t.Errorf("a series starts non-zero: %v", s)
			}
		}
		return
	}
	t.Fatal("rivolt_rivian_unpermitted_total is not registered")
}
