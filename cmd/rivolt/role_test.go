package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func leaselessOn(role string) map[string]string {
	m := map[string]string{"RIVOLT_ROLE": role}
	for _, k := range leaselessRoleRequires {
		m[k] = "1"
	}
	return m
}

func TestRolePlan(t *testing.T) {
	for name, c := range map[string]struct {
		env     map[string]string
		want    rolePlan
		wantErr string
	}{
		"unset is all":         {env: nil, want: rolePlan{Role: roleAll, Leases: true, ForwardOwner: true, LocalFallback: true, PublicRoutes: true, Jobs: true}},
		"all, any case":        {env: map[string]string{"RIVOLT_ROLE": " ALL "}, want: planFor(roleAll)},
		"ingest needs nothing": {env: map[string]string{"RIVOLT_ROLE": "ingest"}, want: rolePlan{Role: roleIngest, Leases: true, ForwardOwner: true, LocalFallback: true}},
		"api":                  {env: leaselessOn("api"), want: rolePlan{Role: roleAPI, PublicRoutes: true}},
		"jobs":                 {env: leaselessOn("jobs"), want: rolePlan{Role: roleJobs, Jobs: true}},
		"unknown":              {env: map[string]string{"RIVOLT_ROLE": "worker"}, wantErr: "must be one of all, ingest, api, jobs"},
		"admin-only + ingest":  {env: map[string]string{"RIVOLT_ROLE": "ingest", "RIVOLT_ADMIN_ONLY": "true"}, wantErr: "admin deployment"},
		"admin-only + all":     {env: map[string]string{"RIVOLT_ADMIN_ONLY": "true"}, want: planFor(roleAll)},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseRolePlan(envOf(c.env))
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err %v, want one containing %q", err, c.wantErr)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %+v err %v, want %+v", got, err, c.want)
			}
		})
	}
}

// api and jobs refuse to start without each of forward, lead mode and single-owner, naming the first one missing.
func TestLeaselessRoleNamesTheFirstMissingSetting(t *testing.T) {
	for _, role := range []string{"api", "jobs"} {
		for i, k := range leaselessRoleRequires {
			m := leaselessOn(role)
			for _, later := range leaselessRoleRequires[i:] {
				delete(m, later)
			}
			_, err := parseRolePlan(envOf(m))
			if want := "RIVOLT_ROLE=" + role + " needs " + k + "=1"; err == nil || err.Error() != want {
				t.Errorf("%s without %v: err %v, want %q", role, leaselessRoleRequires[i:], err, want)
			}
		}
	}
}

// RIVOLT_NO_STANDING_TOKENS needs acquire-on-request, which api and jobs force off: on those roles the ingest pods meet it (they are the ones
// that acquire), so the boot is not refused. On all and ingest it is still required.
func TestNoStandingTokensOnALeaselessRole(t *testing.T) {
	for _, role := range []string{"api", "jobs"} {
		m := allOn()
		for k, v := range leaselessOn(role) {
			m[k] = v
		}
		delete(m, "RIVOLT_ACQUIRE_ON_REQUEST")
		if err := noStandingTokensError(envOf(m)); err != nil {
			t.Errorf("%s + NST without acquire-on-request: %v, want nil", role, err)
		}
	}
	for _, role := range []string{"", "all", "ingest"} {
		m := allOn()
		m["RIVOLT_ROLE"] = role
		delete(m, "RIVOLT_ACQUIRE_ON_REQUEST")
		if err := noStandingTokensError(envOf(m)); err == nil || !strings.Contains(err.Error(), "RIVOLT_ACQUIRE_ON_REQUEST") {
			t.Errorf("role %q + NST without acquire-on-request: %v, want it refused", role, err)
		}
	}
}

func TestProcessRoleGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	registerProcessRole(reg, roleAPI)
	if n, err := testutil.GatherAndCount(reg, "rivolt_process_role"); err != nil || n != len(processRoles) {
		t.Fatalf("series %d err %v, want %d", n, err, len(processRoles))
	}
	want := `
# HELP rivolt_process_role The process role this pod runs as (RIVOLT_ROLE): 1 for its role, 0 for the others.
# TYPE rivolt_process_role gauge
rivolt_process_role{role="all"} 0
rivolt_process_role{role="api"} 1
rivolt_process_role{role="ingest"} 0
rivolt_process_role{role="jobs"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "rivolt_process_role"); err != nil {
		t.Error(err)
	}
}

// The all role's drain is unchanged: /readyz fails first, then the coordinator takes no new lease, then the forward owner stops taking
// calls.
func TestDrainFuncOrder(t *testing.T) {
	var order []string
	drainFunc(func() { order = append(order, "startDrain") },
		func(ctx context.Context) {
			if _, ok := ctx.Deadline(); !ok {
				t.Error("markDraining has no deadline")
			}
			order = append(order, "markDraining")
		},
		func() { order = append(order, "setDraining") }, slog.New(slog.DiscardHandler))()
	if got := strings.Join(order, ","); got != "startDrain,markDraining,setDraining" {
		t.Errorf("order %s", got)
	}
}

// A pod with no coordinator (RIVOLT_ROLE api, jobs) still fails /readyz on SIGUSR1.
func TestDrainFuncWithoutACoordinator(t *testing.T) {
	var order []string
	drainFunc(func() { order = append(order, "startDrain") }, nil, func() { order = append(order, "setDraining") }, slog.New(slog.DiscardHandler))()
	if got := strings.Join(order, ","); got != "startDrain,setDraining" {
		t.Errorf("order %s", got)
	}
}

// The archive settings are read where the archive is used. With RIVOLT_ARCHIVE_ENV set and no keys (what the chart gives ingest), ingest
// boots with the archive off, and every role that uses the archive still refuses the half-configured install.
func TestArchiveConfigPerRole(t *testing.T) {
	getenv := func(k string) string {
		if k == "RIVOLT_ARCHIVE_ENV" {
			return "prod"
		}
		return ""
	}
	for _, r := range processRoles {
		_, on, err := archiveConfig(planFor(r), getenv)
		if r == roleIngest {
			if on || err != nil {
				t.Errorf("ingest: archive on=%v err=%v, want off and no error", on, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: a half-configured archive did not stop the boot", r)
		}
	}
}
