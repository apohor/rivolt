package main

// starterKind says which roles run a background goroutine (zero-downtime design §2.6, P2; S19 9d). Every goroutine this package starts goes
// through rolePlan.start with a name registered in starters; the lint in starters_test.go fails on a bare `go` statement, an unregistered
// name and a registered name no code starts.
type starterKind int

const (
	// startAlways runs on every role: the process itself (servers, signals, drain), request-driven work, and starters whose site already
	// decides by its own setting (the leaseless group-map follow runs only where the coordinator does not).
	startAlways starterKind = iota
	// startRoutes runs where the public routes are mounted (rolePlan.PublicRoutes): caches only those routes read.
	startRoutes
	// startLeases runs where the pod holds group leases (rolePlan.Leases): the coordinator, the forward owner loop, and the work that calls
	// Rivian or reads a live monitor for the groups held.
	startLeases
	// startJobs runs where the background jobs run (rolePlan.Jobs): sweeps, retriers and singletons that read and write our own database.
	startJobs
)

var starters = map[string]starterKind{
	// The process.
	"http_server":           startAlways,
	"http_shutdown":         startAlways,
	"metrics_server":        startAlways,
	"second_signal":         startAlways,
	"drain_watch":           startAlways,
	"drain_watch_leaseless": startAlways,
	// Grants re-applied every 6 h on every replica (idempotent; CNPG creates the roles on its own schedule).
	"db_app_grants":      startAlways,
	"db_repair_grants":   startAlways,
	"db_readonly_grants": startAlways,
	// Drop and count the Rivian clients of groups this pod does not hold: a pod with no leases needs it most.
	"token_release":       startAlways,
	"tokens_unheld_gauge": startAlways,
	"group_map_follow":    startAlways, // the leaseless branch's fence source; the leased branch has the coordinator instead
	"signin_nudge":        startAlways, // one per sign-in: a non-owner forwards the nudge to the owner

	"chargers_warm":   startRoutes,
	"chargers_reload": startRoutes,

	"lease_coordinator":       startLeases,
	"lease_reconcile_trigger": startLeases,
	"forward_owner":           startLeases,
	"vehicle_discovery":       startLeases,
	"push_live":               startLeases,
	"ota_sync":                startLeases,
	"receipt_sync":            startLeases,
	"left_open":               startLeases,
	"tire_low":                startLeases,

	"stripe_cancel_retrier":    startJobs,
	"chargewatch":              startJobs,
	"entitlement_stats":        startJobs,
	"push_notifications_sweep": startJobs,
	"demo_pricing":             startJobs,
	"demo_weather":             startJobs,
	"demo_plans":               startJobs,
	"partition_janitor":        startJobs,
	"drain_metrics":            startJobs,
	"packhealth_backfill":      startJobs,
	"reauth_nudge":             startJobs,
	"idp_delete_retrier":       startJobs,
	"apple_revoke_retrier":     startJobs,
	"inactive_purge":           startJobs,
	"archive_deletions":        startJobs,
	"retention":                startJobs,
	"backfill":                 startJobs,
	"milestones":               startJobs,
	"plug_reminder":            startJobs,
}

// runs reports whether this role starts goroutines of kind k. startAlways is true for the zero plan too, so a test that builds a part
// without a plan still runs its process-level goroutines.
func (p rolePlan) runs(k starterKind) bool {
	switch k {
	case startRoutes:
		return p.PublicRoutes
	case startLeases:
		return p.Leases
	case startJobs:
		return p.Jobs
	}
	return true
}

// start runs fn in a goroutine when this role runs the starter called name, and reports whether it did. An unregistered name panics: the lint
// catches it first, and a starter nobody assigned a role must not run on every role by default.
func (p rolePlan) start(name string, fn func()) bool {
	k, ok := starters[name]
	if !ok {
		panic("rolePlan.start: unregistered starter " + name)
	}
	if !p.runs(k) {
		return false
	}
	go fn()
	return true
}
