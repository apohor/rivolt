package main

import (
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/apohor/rivolt/internal/bg"
)

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

// single is how a starter's work stays correct when every pod of its role runs it (the N-replica audit, Oct 30, rechecked Nov 12: every
// loop that sends, deletes or calls Rivian is lock-, claim- or lease-gated). starter.by names the lock or claim, or says why every pod may run it.
type single int

const (
	singleUnset single = iota
	// singleOwned acts only for the groups this pod holds (the lease fence, or a forward to the group's owner).
	singleOwned
	// singleLock is a named lock or claim: one pod does the work (an advisory lock, SKIP LOCKED), or each effect happens once (a claim row).
	singleLock
	// singleEveryPod is safe on every pod: the state is the pod's own (a cache, its own clients and leases), or the work is idempotent and cheap.
	singleEveryPod
)

// starter is one registered goroutine: where it runs and how it stays single.
type starter struct {
	kind   starterKind
	single single
	by     string
	// tracked starts it with bg.Go, so the shutdown waits for it (bgShutdownWait) as it did before it was registered.
	tracked bool
	// onAdmin: it also runs on the admin deployment (RIVOLT_ADMIN_ONLY, always role all). The admin pod runs no background work of its own:
	// what is marked here is what ran there before the registry, all of it claim-safe or per-pod. The site's own gate stays too.
	onAdmin bool
	// offDemoOnly: it does not run on a demo-only install (RIVOLT_DEMO_ONLY), which sends nothing and has no real users.
	offDemoOnly bool
	// demo: it runs only where the demo account is seeded (RIVOLT_DEMO).
	demo bool
}

var starters = map[string]starter{
	// The process.
	"http_server":           {kind: startAlways, single: singleEveryPod, by: "the process", onAdmin: true},
	"http_shutdown":         {kind: startAlways, single: singleEveryPod, by: "the process", onAdmin: true},
	"metrics_server":        {kind: startAlways, single: singleEveryPod, by: "the process", onAdmin: true},
	"second_signal":         {kind: startAlways, single: singleEveryPod, by: "the process", onAdmin: true},
	"drain_watch":           {kind: startAlways, single: singleEveryPod, by: "the process"},
	"drain_watch_leaseless": {kind: startAlways, single: singleEveryPod, by: "the process"},
	// This pod's copies of the shared state: the flags snapshot, the settings, the Rivian accounts (one batched read per pass).
	"flags_poll":    {kind: startAlways, single: singleEveryPod, by: "an in-memory snapshot of the flags table", onAdmin: true},
	"settings_sync": {kind: startAlways, single: singleEveryPod, by: "an in-memory copy of the settings", tracked: true, onAdmin: true},
	"account_sync":  {kind: startAlways, single: singleEveryPod, by: "this pod's copy of each Rivian account", tracked: true, onAdmin: true},
	// Drop and count the Rivian clients of groups this pod does not hold: a pod with no leases needs it most.
	"token_release":       {kind: startAlways, single: singleEveryPod, by: "this pod's own clients", onAdmin: true},
	"tokens_unheld_gauge": {kind: startAlways, single: singleEveryPod, by: "this pod's own clients"},
	"group_map_follow":    {kind: startAlways, single: singleEveryPod, by: "this pod's map of the groups"},                     // the leaseless branch's fence source; the leased branch has the coordinator instead
	"signin_nudge":        {kind: startAlways, single: singleOwned, by: "a non-owner forwards the nudge to the group's owner"}, // one per sign-in

	"chargers_warm":   {kind: startRoutes, single: singleEveryPod, by: "an in-memory cache of the chargers file", onAdmin: true},
	"chargers_reload": {kind: startRoutes, single: singleEveryPod, by: "an in-memory cache of the chargers file", onAdmin: true},

	"lease_coordinator":       {kind: startLeases, single: singleEveryPod, by: "the ownership protocol: each pod renews and sheds its own leases"},
	"lease_reconcile_trigger": {kind: startLeases, single: singleEveryPod, by: "pokes this pod's coordinator"},
	"forward_owner":           {kind: startLeases, single: singleOwned, by: "answers only the groups this pod holds"},
	"vehicle_discovery":       {kind: startLeases, single: singleLock, by: "advisory rivolt:vehicle_discovery; owner mode: the groups this pod holds", onAdmin: true},
	"push_live":               {kind: startLeases, single: singleLock, by: "claim claimActivityUpdate; reads the owner's live state"},
	"ota_sync":                {kind: startLeases, single: singleLock, by: "claim ClaimOTAFetch per vehicle a day"},
	"receipt_sync":            {kind: startLeases, single: singleLock, by: "a claim per user"},
	"left_open":               {kind: startLeases, single: singleLock, by: "claim leftopen Claim per episode", offDemoOnly: true},
	"tire_low":                {kind: startLeases, single: singleLock, by: "claim tirelow Claim per wheel set (its database claim only)", offDemoOnly: true},

	// Grants re-applied every 6 h (idempotent; CNPG creates the roles on its own schedule, and rivolt migrate grants after each migration).
	"db_app_grants":      {kind: startJobs, single: singleLock, by: "advisory rv_gran, waited for"},
	"db_repair_grants":   {kind: startJobs, single: singleLock, by: "advisory rv_gran, waited for"},
	"db_readonly_grants": {kind: startJobs, single: singleLock, by: "advisory rv_gran, waited for"},

	"stripe_cancel_retrier": {kind: startJobs, single: singleLock, by: "SKIP LOCKED in ClaimDueStripeCancels", onAdmin: true},
	// chargewatch's package is in the ingest module (internal/arch: it reads frames through internal/rivian), but its loop only reads Postgres
	// and sets gauges, so it runs with the jobs. The module says whose code it is; the kind says where it runs.
	"chargewatch":              {kind: startJobs, single: singleEveryPod, by: "read-only gauges", onAdmin: true},
	"entitlement_stats":        {kind: startJobs, single: singleEveryPod, by: "read-only gauges", onAdmin: true},
	"push_notifications_sweep": {kind: startJobs, single: singleEveryPod, by: "one idempotent DELETE an hour", onAdmin: true, offDemoOnly: true},
	"demo_pricing":             {kind: startJobs, single: singleLock, by: "advisory lock pricing (demo onOneReplica)", onAdmin: true, demo: true},
	"demo_weather":             {kind: startJobs, single: singleLock, by: "advisory lock weather (demo onOneReplica)", onAdmin: true, demo: true},
	"demo_plans":               {kind: startJobs, single: singleLock, by: "advisory lock plans (demo onOneReplica)", onAdmin: true, demo: true},
	"partition_janitor":        {kind: startJobs, single: singleLock, by: "advisory rv_parj"},
	"drain_metrics":            {kind: startJobs, single: singleLock, by: "advisory rivolt-drain-metrics (leader)"},
	"packhealth_backfill":      {kind: startJobs, single: singleLock, by: "advisory rv_pkhl"},
	"reauth_nudge":             {kind: startJobs, single: singleLock, by: "claim ClaimReauthNudge"},
	"rivian_expiry":            {kind: startJobs, single: singleLock, by: "engage claim per (user, expiry, days)", tracked: true},
	"r2_arrival":               {kind: startJobs, single: singleLock, by: "engage claim per (user, month)", tracked: true},
	"idp_delete_retrier":       {kind: startJobs, single: singleLock, by: "claim ClaimDueIdPDeleteFailures", onAdmin: true},
	"apple_revoke_retrier":     {kind: startJobs, single: singleLock, by: "claim ClaimDueAppleRevokes", onAdmin: true},
	"inactive_purge":           {kind: startJobs, single: singleLock, by: "claim ClaimInactiveWarning and a row lock before each delete"},
	"archive_deletions":        {kind: startJobs, single: singleLock, by: "SKIP LOCKED on the deletion queue"},
	"retention":                {kind: startJobs, single: singleLock, by: "advisory rv_rete; its small DELETEs are idempotent"},
	"backfill":                 {kind: startJobs, single: singleLock, by: "advisory backfill (internal/singleton)"},
	"milestones":               {kind: startJobs, single: singleLock, by: "insert ON CONFLICT DO NOTHING, then the engage claim", offDemoOnly: true},
	"plug_reminder":            {kind: startJobs, single: singleLock, by: "engage claim per (account, kind, local date)", offDemoOnly: true},
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
	st, ok := starters[name]
	if !ok {
		panic("rolePlan.start: unregistered starter " + name)
	}
	if !p.starts(st) {
		bootStarts.note(name, false)
		return false
	}
	bootStarts.note(name, true)
	if st.tracked {
		bg.Go(fn)
	} else {
		go fn()
	}
	return true
}

// starts reports whether this pod runs st: its role runs the kind, and the install allows it (admin deployment, demo-only, demo).
func (p rolePlan) starts(st starter) bool {
	return p.runs(st.kind) && (!p.AdminOnly || st.onAdmin) && (!p.DemoOnly || !st.offDemoOnly) && (!st.demo || p.Demo)
}

// bootStarts records which starters this process started and which it skipped, for the one boot line (logStarters).
var bootStarts startLog

type startLog struct {
	mu               sync.Mutex
	started, skipped []string
}

func (l *startLog) note(name string, started bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if started {
		l.started = append(l.started, name)
	} else {
		l.skipped = append(l.skipped, name)
	}
}

// logStarters logs, once at boot, the starters this pod started, the ones its role or install skipped, and the ones no site reached
// (a setting off, no database). A starter that begins later (one sign-in nudge) shows in the third list.
func logStarters(log *slog.Logger, p rolePlan, l *startLog) {
	l.mu.Lock()
	started, skipped := append([]string(nil), l.started...), append([]string(nil), l.skipped...)
	l.mu.Unlock()
	seen := map[string]bool{}
	for _, n := range append(append([]string(nil), started...), skipped...) {
		seen[n] = true
	}
	var unreached []string
	for n := range starters {
		if !seen[n] {
			unreached = append(unreached, n)
		}
	}
	sort.Strings(started)
	sort.Strings(skipped)
	sort.Strings(unreached)
	log.Info("role starters", "role", string(p.Role), "started", strings.Join(started, ","), "skipped", strings.Join(skipped, ","),
		"not_reached", strings.Join(unreached, ","))
}
