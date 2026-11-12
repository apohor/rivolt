package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/apohor/rivolt/internal/archive"
)

// processRole is RIVOLT_ROLE: which part of the work this process does (zero-downtime design §2.6, P2). One binary and one image; the
// role decides what starts. `all` (the default) is today's process: every subsystem, every route.
type processRole string

const (
	roleAll    processRole = "all"
	roleIngest processRole = "ingest" // Rivian sessions, leases, the recorder; no public routes and no jobs
	roleAPI    processRole = "api"    // public routes; holds no leases, so it never runs a Rivian subscription, and runs no jobs
	roleJobs   processRole = "jobs"   // background work; holds no leases and serves no public routes
)

var processRoles = []processRole{roleAll, roleIngest, roleAPI, roleJobs}

// leaselessRoleRequires are the settings a role that holds no leases (api, jobs) depends on:
//   - FORWARD: a call that needs Rivian goes to the group's owner; with forward off the pod would call Rivian itself as a non-owner.
//   - GROUP_LEAD: lead mode makes the monitor registry and the live fence read every group this pod does not hold as Lost
//     (main.go RequireFence). Without it a pod with no coordinator has no fence, and the boot hydrate's monitors would stream and write
//     for every vehicle: two sessions per user, the Oct 28 re-login shape.
//   - SINGLE_OWNER: needs_reauth raises are the owner's; a pod that never owns a group must not raise them.
var leaselessRoleRequires = []string{
	"RIVOLT_FORWARD",
	"RIVOLT_GROUP_LEAD",
	"RIVOLT_SINGLE_OWNER",
}

// rolePlan is what a role starts. main.go reads these fields at each gate; a later P2 PR adds a field rather than a string compare.
type rolePlan struct {
	Role processRole
	// Leases: the lease coordinator (its passes, the release signal, the reconcile trigger). Off, this pod writes no lease row.
	Leases bool
	// ForwardOwner: the loop that answers calls forwarded to the groups this pod holds.
	ForwardOwner bool
	// LocalFallback: the requester may run a call on this pod when it cannot be forwarded (fall-soft), or acquire a free group for it
	// (RIVOLT_ACQUIRE_ON_REQUEST). Off, each of those answers 503 Retry-After (outcome no_local).
	LocalFallback bool
	// PublicRoutes: the full router. Off, the router has the probes only (/healthz, /readyz, /api/health).
	PublicRoutes bool
	// Jobs: the sweeps, retriers and singletons on our own database (starters.go startJobs). Off on ingest and api.
	Jobs bool
	// The install, not the role (main.go sets them after parseRolePlan): the admin deployment (RIVOLT_ADMIN_ONLY), a demo-only install
	// (RIVOLT_DEMO_ONLY) and a seeded demo account (RIVOLT_DEMO). rolePlan.start reads them with each starter's onAdmin, offDemoOnly and demo.
	AdminOnly, DemoOnly, Demo bool
}

func planFor(r processRole) rolePlan {
	p := rolePlan{Role: r, Leases: true, ForwardOwner: true, LocalFallback: true, PublicRoutes: true, Jobs: true}
	switch r {
	case roleIngest:
		p.PublicRoutes, p.Jobs = false, false
	case roleAPI:
		p.Leases, p.ForwardOwner, p.LocalFallback, p.Jobs = false, false, false, false
	case roleJobs:
		p.Leases, p.ForwardOwner, p.LocalFallback, p.PublicRoutes = false, false, false, false
	}
	return p
}

// parseRolePlan reads RIVOLT_ROLE and checks what the role depends on. An unknown role, a role on the admin deployment, or a leaseless role
// without its settings is an error naming the first problem; main exits 1 on it (a pod never runs as something other than what it was told).
func parseRolePlan(getenv func(string) string) (rolePlan, error) {
	v := strings.ToLower(strings.TrimSpace(getenv("RIVOLT_ROLE")))
	if v == "" {
		v = string(roleAll)
	}
	r := processRole(v)
	known := false
	for _, k := range processRoles {
		known = known || k == r
	}
	if !known {
		return rolePlan{}, fmt.Errorf("RIVOLT_ROLE (process role) must be one of all, ingest, api, jobs")
	}
	p := planFor(r)
	if r == roleAll {
		return p, nil
	}
	if v := getenv("RIVOLT_ADMIN_ONLY"); v == "1" || strings.EqualFold(v, "true") {
		return rolePlan{}, errors.New("RIVOLT_ROLE (process role) must be all on the admin deployment (RIVOLT_ADMIN_ONLY)")
	}
	if !p.Leases {
		for _, k := range leaselessRoleRequires {
			if getenv(k) != "1" {
				return rolePlan{}, fmt.Errorf("RIVOLT_ROLE=%s needs %s=1", r, k)
			}
		}
	}
	return p, nil
}

// roleLeaseless reports whether getenv's RIVOLT_ROLE is one that holds no leases. An unparsable role reads false: parseRolePlan has already
// stopped the boot for it.
func roleLeaseless(getenv func(string) string) bool {
	p, err := parseRolePlan(getenv)
	return err == nil && !p.Leases
}

// registerProcessRole exports rivolt_process_role{role}: 1 for this pod's role, 0 for the others, so a mis-set pod shows in Prometheus.
func registerProcessRole(reg prometheus.Registerer, r processRole) {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "rivolt_process_role",
		Help: "The process role this pod runs as (RIVOLT_ROLE): 1 for its role, 0 for the others.",
	}, []string{"role"})
	reg.MustRegister(g)
	for _, k := range processRoles {
		v := 0.0
		if k == r {
			v = 1
		}
		g.WithLabelValues(string(k)).Set(v)
	}
}

// archiveConfig reads the telemetry archive's settings on the roles that use them: the archiver and the deletions run with the jobs, the
// backup reader and the restorer with the public routes. A role with neither (ingest) leaves the archive off without reading it, so it boots
// without the archive keys: the chart mounts them on api and jobs only (#1873), and RIVOLT_ARCHIVE_ENV in the shared config would otherwise
// stop its boot.
func archiveConfig(p rolePlan, getenv func(string) string) (archive.Config, bool, error) {
	if !p.Jobs && !p.PublicRoutes {
		return archive.Config{}, false, nil
	}
	return archive.ConfigFromEnv(getenv)
}
