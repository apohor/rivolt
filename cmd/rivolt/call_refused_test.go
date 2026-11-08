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

// The Rivian latency histogram has a child per operation and the reconnect counter one per reason, all present from boot (so the first sample is
// an increase and a rate over a quiet period reads 0), and the label lists are the client's.
func TestRivianLatencyAndReconnectSeriesExistFromBoot(t *testing.T) {
	if !reflect.DeepEqual(rivian.WSReconnectReasons, metrics.RivianWSReconnectReasons) {
		t.Fatalf("rivian.WSReconnectReasons %v differs from metrics.RivianWSReconnectReasons %v", rivian.WSReconnectReasons, metrics.RivianWSReconnectReasons)
	}
	mfs, err := metrics.New().Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	wantOps := len(rivian.PermitOps) - 1 // every operation but ws_dial
	for _, mf := range mfs {
		switch mf.GetName() {
		case "rivolt_rivian_request_seconds":
			if len(mf.GetMetric()) != wantOps {
				t.Errorf("request_seconds has %d children, want %d (one per GraphQL operation)", len(mf.GetMetric()), wantOps)
			}
			for _, m := range mf.GetMetric() {
				if m.GetHistogram().GetSampleCount() != 0 || len(m.GetHistogram().GetBucket()) != 8 {
					t.Errorf("a child starts with %d samples and %d buckets, want 0 and 8", m.GetHistogram().GetSampleCount(), len(m.GetHistogram().GetBucket()))
				}
			}
		case "rivolt_rivian_ws_reconnects_total":
			if len(mf.GetMetric()) != len(rivian.WSReconnectReasons) {
				t.Errorf("ws_reconnects has %d series, want %d", len(mf.GetMetric()), len(rivian.WSReconnectReasons))
			}
		}
	}
}
