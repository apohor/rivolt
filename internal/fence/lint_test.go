package fence_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The unguarded-write lint (single-owner design 4.3, test 9): every function in a non-test file
// under internal/ and cmd/ that writes must carry the fence (the rivolt_lease_ok guard or a fence
// token from the context), or be listed here with a reason. Keys are "<dir>.<Recv>.<Func>", the
// dir relative to internal/ ("db", "repair/phantom") or "cmd/<name>".
//
// notYetGuarded is the work list of the enforcement PRs. It only shrinks: each PR that guards a
// write removes its entry, and the test fails for an entry that is guarded already, so the list
// cannot go stale. The last enforcement PR empties it and flips enforcementComplete.
// notOwnerSide is permanent: writes that are not made by the group's owner (user actions,
// imports, repairs, maintenance jobs, sign-in and admin), or that carry no state of the owner's
// account (the lease protocol itself, shared caches), which no lease guards.

const enforcementComplete = true

// deferredToM4 holds owner writes that M2 deliberately leaves unguarded and M4 must guard: they are
// not notOwnerSide (from M4 on only the owner makes them) and do not block enforcementComplete.
// m4Complete closes it as enforcementComplete closes notYetGuarded.
const m4Complete = false

var deferredToM4 = map[string]string{
	// needs_reauth (DA, Oct 30): through M2–M3 non-owners still call Rivian and can see a genuine
	// 401, so an epoch guard would block a true raise. The version guard (#1230) holds until then;
	// the epoch joins it on the design's M4 checklist, once only the owner calls Rivian.
	"db.RaiseNeedsReauth":          "non-owners still call Rivian until M4; version-conditional (#1230) until then",
	"db.RaiseNeedsReauthIfCurrent": "non-owners still call Rivian until M4; version-conditional (#1230) until then",
	"db.SetNeedsReauth":            "non-owners still call Rivian until M4; version-conditional (#1230) until then",
}

// The guard is context-based (a fence token in ctx), so a function that owners and non-owners both
// call is guarded for the owner's calls and unguarded for the rest: it belongs in notYetGuarded
// until it takes the token, whoever else calls it.
var notYetGuarded = map[string]string{}

// notOwnerSide is permanent. Each entry names who calls it.
var notOwnerSide = map[string]string{
	"launchnotice.PGDated.PrepareFounding": "singleton maintenance job (the hourly dated-notice sweep, replica-safe by a row claim): extends a founding year that is under 30 days from its end and claims the notice email in one transaction; an account's plan is not a vehicle group's owner state",
	"launchnotice.Store.EnsureAny":         "singleton maintenance job (the hourly dated-notice sweep): the in-app row of a renewal reminder",
	"launchnotice.Store.ClaimEmailAny":     "singleton maintenance job (the hourly dated-notice sweep): the email claim of a renewal reminder",
	"applebill.Handler.apply":              "Apple's signed notification, applied once (a database row) to the entitlement table: a billing event, not a vehicle group's owner state",
	"applebill.PurgeEvents":                "retention job (the daily sweep of old notification ids)",
	"applebill.Transaction.link":           "user action (api: the signed-in account reports its own purchase)",
	"applebill.applyDecision":              "Apple's notification or the account's own report applied to the entitlement table (see Handler.apply and Transaction.link)",
	"launchnotice.Store.Ensure":            "operator command (rivolt launch-notice): a one-shot run by an operator, not by a group's owner",
	"launchnotice.Store.ClaimEmail":        "operator command (rivolt launch-notice): the email claim of that one-shot run",
	"launchnotice.Store.MarkSent":          "operator command (rivolt launch-notice): records the notice email as sent",
	"launchnotice.Store.ReleaseClaim":      "operator command (rivolt launch-notice): gives a failed email's claim back",
	"launchnotice.Store.Dismiss":           "user action (api/notices.go: the account hides its own banner)",
	"engage.pgStore.Mark":                  "bookkeeping of a claim that was already guarded (status sending -> sent/failed); a refusal here would leave the row stuck in 'sending'",
	"push.Store.CreateNotification":        "the notification text of a send; reached only from hooks the recorder gates on the fence (StateMonitor.hookFence) and from user actions, never on its own",
	"push.Store.RegisterActivity":          "user action (api/push_apns.go: the app starts a Live Activity)",
	"push.Store.EndActivity":               "user action (api/push_apns.go)",
	"push.Store.SetAPNsLive":               "user action (api/push_apns.go: the Live Activity switch)",
	"push.Store.claimActivityUpdate":       "interval claim of the RunLive loop; the send is decided by the fenced live source (cmd/rivolt mayPushFor: no figures unless the pod holds the vehicle's group), and the activity's end goes through the gated close hook",
	"samples.Store.Reset":                  "user action (reset data)",
	"samples.Store.DeleteBefore":           "user action (delete history before a month)",
	"samples.Thinner.thin":                 "singleton maintenance job",
	"samples.Thinner.dropExpiredFull":      "singleton maintenance job",
	"samples.Thinner.rethinPair":           "singleton maintenance job",
	"drives.Store.Reset":                   "user action (reset data)",
	"drives.Store.DeleteBefore":            "user action (delete history before a month)",
	"drives.Store.SetTrailerOverride":      "user action",
	"charges.Store.Reset":                  "user action (reset data)",
	"charges.Store.DeleteBefore":           "user action (delete history before a month)",
	"charges.Store.DeleteByExternalID":     "user action / import",
	"charges.Store.RestampPlace":           "user action",
	"charges.Store.UndoRestamp":            "user action",
	"charges.Store.SetLocationOverride":    "user action",
	"charges.Store.SetPlaceOverride":       "user action",
	"charges.Store.ApplyNetworkRate":       "user action (api/charges.go: the network rate button)",
	"charges.Store.ConfirmNetwork":         "user action (api/charges.go)",
	"charges.Store.MarkFree":               "user action (api/charges.go)",
	"charges.Store.UpdatePricing":          "user action (api/charges.go; also the demo seeder)",
	"charges.Store.SetNetworkPrice":        "user action",
	"engage.pgStore.Purge":                 "retention job",
	"push.PurgeActivities":                 "retention job (cmd/rivolt/main.go)",
	"push.PurgeNotifications":              "retention job (cmd/rivolt/main.go)",
	"push.Store.Delete":                    "user action (unsubscribe)",
	"push.Store.DeleteAPNsDevice":          "user action (device removal)",
	"push.Store.deleteAPNsDeviceByID":      "cleanup of a dead device token",
	"push.Store.deleteActivityByID":        "cleanup of a dead activity token",
	"push.Store.SaveVAPID":                 "one-time key setup",
	"push.Store.SetAPNsLeftOpen":           "user action (settings)",
	"push.Store.SetAPNsOptIns":             "user action (settings)",
	"push.Store.SetAPNsPrefs":              "user action (settings)",
	"push.Store.SetLeftOpen":               "user action (settings)",
	"push.Store.SetOptIns":                 "user action (settings)",
	"push.Store.SwitchOffAll":              "user action (settings)",
	"push.Store.Upsert":                    "user action (subscribe)",
	"push.Store.UpsertAPNsDevice":          "user action (device registration)",
	"push.Store.touchAPNsDevice":           "device check-in from the app",
	"packhealth.Store.BackfillAll":         "hourly maintenance job, one replica at a time (advisory lock, #1312)",
	"leases.Store.AcquireGroups":           "lease protocol (open-session follow, outside the share; #1310)",
	"packhealth.Store.Upsert":              "only reached from BackfillAll via BackfillVehicle: an idempotent re-derivation from stored charges, no owner write path",
	"receipts.Store.purge":                 "user action and retention job (DeleteForUser / opt-out purge)",
	"secrets.Store.Delete":                 "user action (logout, delete, disconnect)",
	"secrets.Store.Put":                    "user and admin actions: Rivian sign-in and MFA (api/rivian.go), the admin session refresh, the Apple sign-in token, the demo seed; no owner path persists a session (TestOwnerPackagesDoNotPersistSessions)",
	"secrets.Store.PutTxVersioned":         "as Put: SaveRivianSession's transaction for a sign-in or sign-out",
	"secrets.setSignedIn":                  "as Put: the sign-in time, in the same sign-in transaction",
	"secrets.Store.DeleteTx":               "user action (logout, delete, disconnect)",
	"secrets.RivianSessionLister.backfill": "one-time sign-in time backfill",

	// The lease protocol itself: its writes are the epoch CAS that the guard reads.
	"leases.Store.Acquire":         "lease protocol (epoch CAS)",
	"leases.Store.LeadGroups":      "lease protocol (epoch CAS)",
	"leases.Store.MarkDraining":    "lease protocol (pod draining flag)",
	"leases.Store.Release":         "lease protocol (epoch CAS)",
	"leases.Store.ReleaseAll":      "lease protocol (epoch CAS)",
	"leases.Store.ReleaseGroup":    "lease protocol (epoch CAS)",
	"leases.Store.releaseGroups":   "lease protocol (epoch CAS)",
	"leases.Store.Renew":           "lease protocol (epoch CAS)",
	"db.VehicleResolver.Resolve":   "identity row only (user, Rivian id; ON CONFLICT changes nothing), no vehicle state; the guarded write that follows is what the lease decides",
	"db.run":                       "migrations at server start",
	"weather.Cache.Put":            "derived cache of public weather data; idempotent; no user-visible effect if a zombie writes it",
	"weather.Cache.PutSeries":      "derived cache of public weather data; idempotent; no user-visible effect if a zombie writes it",
	"cmd/rivolt.runReprice":        "operator CLI",
	"cmd/rivolt.runRepriceRestore": "operator CLI",
	"repair.Apply":                 "operator CLI (repair)",
	"repair/blips.Apply":           "operator CLI (repair)",
	"repair/routegaps.Apply":       "operator CLI (repair)",
	"repair/routegaps.Restore":     "operator CLI (repair undo)",
	"repair/chargeenergy.Apply":    "operator CLI (repair)",
	"repair/chargeenergy.Restore":  "operator CLI (repair undo)",
	"repair/phantom.Apply":         "operator CLI (repair)",
	"repair/phantom.restoreRows":   "operator CLI (repair undo)",
	"repair/r2.Apply":              "operator CLI (repair)",
	"archive.Archiver.archiveOne":  "N19 archive job (singleton)",
	"archive.loadObject":           "N19 archive job: COPY into its own staging table",
	"archive.Archiver.dataKey":     "N19 archive job: the user's archive data key",
	"archive.Deleter.Run":          "N19 archive deletion job",
	"archive.FlagMismatched":       "thinning job (singleton maintenance)",
	"archive.MarkRearchive":        "in the samples write's own transaction (the guarded insert decides whether it commits) and the thinning job",
	"archive.Restorer.Restore":     "archive restore: admin action and operator CLI, under the maintenance lock",
	"demo.Seed":                    "demo account seed at start",
	"demo.SeedPlans":               "demo account seed at start",
	"demo.ApplySampleWeather":      "demo account seed at start",

	// Accounts, sign-in, sessions and admin.
	"db.AcceptTerms":                          "user action (terms)",
	"db.CreateUser":                           "sign-up",
	"db.EnsureUserVerified":                   "sign-in",
	"db.SetDisabled":                          "admin action",
	"db.SetRole":                              "admin action",
	"db.SetOnboardingCompleted":               "user action (onboarding)",
	"db.SetEngagementEmailsOff":               "user action (unsubscribe)",
	"db.ClearR2ArrivalMonthFor":               "per-user engagement setting deleted by the arrival-month job after its email is claimed; any replica, no group state",
	"db.ClearEndedR2ArrivalMonths":            "engagement setting past its month, deleted by the arrival-month job; any replica, no group state",
	"db.ClearR2ArrivalMonth":                  "user action (the reminder email's one-click delete)",
	"db.SetRivianExpiryEmailsOff":             "user action (unsubscribe)",
	"db.SetVehicleImageURL":                   "user request (/api/vehicles)",
	"db.SetVehicleProfile":                    "user action (vehicle profile)",
	"db.ResetUserArchive":                     "user action (reset data)",
	"db.DeleteUserArchiveBefore":              "user action (delete history before a month)",
	"db.DeleteUserHistoryBefore":              "user action (delete history before a month)",
	"histfloor.Raise":                         "user action (delete history: raises the floor before the deletes)",
	"stripebill.Handler.apply":                "Stripe webhook (internal/stripebill), an HTTP handler applying Stripe's signed events: not an owner-side recorder write",
	"stripebill.Handler.chargeRefunded":       "Stripe webhook (internal/stripebill), an HTTP handler applying Stripe's signed events: not an owner-side recorder write",
	"stripebill.Handler.subscriptionChanged":  "Stripe webhook (internal/stripebill), an HTTP handler applying Stripe's signed events: not an owner-side recorder write",
	"stripebill.linkCustomer":                 "Stripe webhook (internal/stripebill), an HTTP handler applying Stripe's signed events: not an owner-side recorder write",
	"stripebill.PurgeEvents":                  "retention job (stripe webhook event ids older than 30 days), not an owner-side write",
	"db.StartTrial":                           "sign-up time account write (a brand-new account's trial row), not owner-side",
	"entitlements.ApplyFounding":              "operator CLI (rivolt entitlements-founding), not owner-side",
	"db.RecordServerEvent":                    "analytics event of a user action (sign-up, connect)",
	"db.deleteUserAccountTx":                  "user action / inactive purge (account deletion)",
	"db.deleteUserTx":                         "user action / inactive purge (account deletion)",
	"db.queueArchiveDeletionTx":               "account deletion",
	"db.AppleRevokeFailedAgain":               "account-deletion Apple revoke retry job",
	"db.CancelAppleRevoke":                    "account-deletion Apple revoke retry job",
	"db.ClaimDueAppleRevokes":                 "account-deletion Apple revoke retry job",
	"db.ExpireAppleRevokes":                   "account-deletion Apple revoke retry job",
	"db.RecordAppleRevokeFailure":             "account deletion",
	"db.ClaimDueIdPDeleteFailures":            "account-deletion IdP retry job",
	"db.ClearIdPDeleteFailure":                "account-deletion IdP retry job",
	"db.RecordIdPDeleteFailure":               "account deletion",
	"db.ResolveIdPDeleteFailure":              "account-deletion IdP retry job",
	"db.CancelInactiveWarnings":               "inactive-account purge job",
	"db.ClaimInactiveWarning":                 "inactive-account purge job (claim per user)",
	"db.MarkInactiveWarned":                   "inactive-account purge job",
	"db.ClaimReauthNudge":                     "re-auth nudge email job (claim per user)",
	"db.ClaimSetupNudge":                      "onboarding email job (claim per user)",
	"db.ClaimWelcome":                         "onboarding email job (claim per user)",
	"sessions.Store.Create":                   "sign-in",
	"sessions.Store.Lookup":                   "session use (last seen)",
	"sessions.Store.PurgeExpired":             "retention job",
	"sessions.Store.Revoke":                   "user action (sign out)",
	"sessions.Store.RevokeAll":                "user / admin action",
	"sessions.Store.RevokeAllExcept":          "user action",
	"sessions.Store.RevokeByToken":            "user action (sign out)",
	"sessions.Store.RevokeOthers":             "user action",
	"sessions.Store.RevokeOwned":              "user action",
	"passkeys.PGStore.Add":                    "user action (passkey)",
	"passkeys.PGStore.Delete":                 "user action (passkey)",
	"passkeys.PGStore.PurgeCeremonies":        "retention job",
	"passkeys.PGStore.RecordUse":              "sign-in",
	"passkeys.PGStore.Rename":                 "user action (passkey)",
	"passkeys.PGStore.SaveCeremony":           "sign-in ceremony",
	"passkeys.PGStore.TakeCeremony":           "sign-in ceremony",
	"passkeys.PGStore.UserHandle":             "sign-in ceremony",
	"appauth.Store.Begin":                     "app sign-in",
	"appauth.Store.Finish":                    "app sign-in",
	"appauth.Store.Purge":                     "retention job",
	"appauth.Store.Redeem":                    "app sign-in",
	"appleid.Store.Link":                      "sign-in (Apple)",
	"appleid.Store.MarkSeen":                  "sign-in (Apple)",
	"appleid.Store.PurgeSeen":                 "retention job",
	"appleid.Store.SetEmailUndeliverable":     "Apple email relay notice",
	"appleid.Store.Unsee":                     "sign-in (Apple)",
	"signupgate.Allow":                        "sign-up",
	"signuprequests.Store.ApproveWithToken":   "admin action (sign-up request)",
	"signuprequests.Store.ConsumeToken":       "sign-up",
	"signuprequests.Store.Create":             "sign-up request",
	"signuprequests.Store.PurgeStale":         "retention job",
	"signuprequests.Store.Reject":             "admin action (sign-up request)",
	"adminaudit.Store.Begin":                  "admin audit",
	"adminaudit.Store.Finish":                 "admin audit",
	"adminaudit.Store.NoteImpersonationStart": "admin audit",
	"adminaudit.Store.Purge":                  "retention job",
	"flags.Store.SetAICallCap":                "admin action (flags)",
	"flags.Store.SetKillSwitch":               "admin action (flags)",
	"flags.Store.SetSignupCap":                "admin action (flags)",
	"flags.Store.SetStatementEmail":           "admin action (flags)",
	"flags.Store.SetTripPlanner":              "admin action (flags)",

	// User data, settings and the API's own tables.
	"settings.SeedDistanceUnit":    "user action (settings)",
	"settings.Store.Delete":        "user action (settings)",
	"settings.Store.Set":           "user action (settings)",
	"settings.Store.SetMany":       "user action (settings)",
	"appsettings.Store.Delete":     "admin action (install settings)",
	"appsettings.Store.Set":        "admin action (install settings)",
	"trips.Store.Create":           "user action (trips)",
	"trips.Store.Delete":           "user action (trips)",
	"trips.Store.Update":           "user action (trips)",
	"vehiclelease.Store.Delete":    "user action (vehicle lease)",
	"vehiclelease.Store.Put":       "user action (vehicle lease)",
	"milestones.PGStore.Dismiss":   "user action",
	"milestones.pgStore.MarkReady": "one-off backfill flag (idempotent on every replica)",
	"leftopen.pgSource.Prune":      "retention job",
	"statement.pgStore.Claim":      "monthly statement email job (no token, as the design says)",
	"statement.pgStore.Mark":       "monthly statement email job",
	"statement.pgStore.Purge":      "retention job",
	"statement.pgStore.SetPrefs":   "user action (statement settings)",
	"ai.NewRecorder":               "AI usage ledger setup at start (not Rivian state)",
	"ai.Recorder.Record":           "AI usage ledger (user requests and the recap job; not Rivian state)",
	"aibudget.Store.TryConsume":    "AI budget of a user request",
	"api.engagementAPI.put":        "user action (engagement settings)",
	"api.pgEventsSink.Insert":      "analytics events from the app",
	"api.purgeEvents":              "retention job",
	"api.pgGaps.claim":             "user action (backup / restore rate limit)",
	"api.pgGaps.clear":             "user action (backup / restore rate limit)",
	"api.pgPlanLinks.Link":         "user action (trip plan link)",
	"api.pgPlanLinks.Unlink":       "user action (trip plan link)",
	"api.saveDriveEfficiency":      "user request (drive efficiency cache)",
	"api.upsertDriveAnalysis":      "user action (restore from backup)",
	"api.upsertDriveRecap":         "user action (restore from backup)",
	"api.upsertMilestone":          "user action (restore from backup)",
	"api.upsertSavedTrip":          "user action (restore from backup)",
	"api.weeklyUnsub.post":         "user action (unsubscribe)",
}

// Not SQL, so not in this lint: the Redis live-state writes (the epoch CAS PR) and the Rivian calls
// (the deadline-context PR) are tracked as their own enforcement steps.

var writeSQL = regexp.MustCompile(`(?is)\b(INSERT\s+INTO\s+[a-z_]+|UPDATE\s+[a-z_]+(\s+[a-z_]+)?\s+SET\b|DELETE\s+FROM\s+[a-z_]+|MERGE\s+INTO\s+[a-z_]+|(?-i:COPY\s+[a-z_]+\s*(\(|FROM\b)))`)

// guarded reports whether a function body (or a statement it uses) carries the fence.
func guarded(src string) bool {
	return strings.Contains(src, "rivolt_lease_ok") || strings.Contains(src, "fence.From(") || strings.Contains(src, "guardedInsert") || strings.Contains(src, "execOwnerWrite(") || strings.Contains(src, "guardedChargeUpsert") || strings.Contains(src, "fence.Exec(")
}

// writers returns "pkg.Recv.Func" for every function under internal/ and cmd/ whose string
// literals contain write SQL, or that use a package-level constant or variable holding some, and
// whether it is guarded. Write SQL anywhere else (outside a function and not the value of a named
// constant or variable) fails the test: it could not be attributed to a writer.
func writers(t *testing.T) map[string]bool {
	t.Helper()
	type file struct {
		af   *ast.File
		fset *token.FileSet
		src  string
	}
	byPkg := map[string][]file{}
	for _, f := range sources(t) {
		fset := token.NewFileSet()
		af, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		k := pkgKey(f)
		byPkg[k] = append(byPkg[k], file{af, fset, mustRead(t, f)})
	}
	out := map[string]bool{}
	for d, files := range byPkg {
		// Package-level constants and variables holding write SQL: name -> guarded.
		consts := map[string]bool{}
		for _, f := range files {
			for _, decl := range f.af.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				// Every literal in a named value (a concatenation too) belongs to that name; the
				// value's whole expression decides whether it is guarded.
				named := map[*ast.BasicLit]string{}
				valueSrc := map[string]string{}
				for _, spec := range gd.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, v := range vs.Values {
						if i >= len(vs.Names) {
							break
						}
						name := vs.Names[i].Name
						valueSrc[name] = f.src[f.fset.Position(v.Pos()).Offset:f.fset.Position(v.End()).Offset]
						ast.Inspect(v, func(n ast.Node) bool {
							if bl, ok := n.(*ast.BasicLit); ok {
								named[bl] = name
							}
							return true
						})
					}
				}
				ast.Inspect(gd, func(n ast.Node) bool {
					bl, ok := n.(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING || !writeSQL.MatchString(bl.Value) {
						return true
					}
					if name, ok := named[bl]; ok {
						consts[name] = guarded(valueSrc[name])
					} else {
						t.Errorf("%s: write SQL outside a function that is not a named constant or variable: the lint cannot attribute it", f.fset.Position(bl.Pos()))
					}
					return true
				})
			}
		}
		used := map[string]bool{}
		for _, f := range files {
			for _, decl := range f.af.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				writes, viaGuarded := false, false
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.BasicLit:
						if n.Kind == token.STRING && writeSQL.MatchString(n.Value) {
							writes = true
						}
					case *ast.Ident:
						if g, ok := consts[n.Name]; ok {
							used[n.Name] = true
							writes = true
							viaGuarded = viaGuarded || g
						}
					}
					return true
				})
				if !writes {
					continue
				}
				name := fd.Name.Name
				if fd.Recv != nil && len(fd.Recv.List) > 0 {
					typ := fd.Recv.List[0].Type
					if s, ok := typ.(*ast.StarExpr); ok {
						typ = s.X
					}
					if id, ok := typ.(*ast.Ident); ok {
						name = id.Name + "." + name
					}
				}
				body := f.src[f.fset.Position(fd.Pos()).Offset:f.fset.Position(fd.End()).Offset]
				out[d+"."+name] = viaGuarded || guarded(body)
			}
		}
		for name := range consts {
			if !used[name] {
				t.Errorf("%s.%s holds write SQL that no function in the package uses: the lint cannot attribute it", d, name)
			}
		}
	}
	return out
}

func TestEveryOwnerWriteIsGuardedOrListed(t *testing.T) {
	found := writers(t)
	var bad []string
	for name, isGuarded := range found {
		_, pending := notYetGuarded[name]
		_, exempt := notOwnerSide[name]
		_, deferred := deferredToM4[name]
		switch {
		case isGuarded && pending:
			bad = append(bad, fmt.Sprintf("%s is guarded now: remove it from notYetGuarded", name))
		case isGuarded && exempt:
			bad = append(bad, fmt.Sprintf("%s is guarded: it does not belong in notOwnerSide", name))
		case isGuarded && deferred:
			bad = append(bad, fmt.Sprintf("%s is guarded now: remove it from deferredToM4", name))
		case !isGuarded && !pending && !exempt && !deferred:
			bad = append(bad, fmt.Sprintf("%s writes without the fence: guard it (rivolt_lease_ok in the statement, token from fence.From), or list it with a reason", name))
		}
	}
	lists := map[string]int{}
	for _, m := range []map[string]string{notYetGuarded, notOwnerSide, deferredToM4} {
		for name := range m {
			lists[name]++
		}
	}
	for name, n := range lists {
		if n > 1 {
			bad = append(bad, fmt.Sprintf("%s is in %d lists: keep it in one", name, n))
		}
	}
	for _, m := range []map[string]string{notYetGuarded, notOwnerSide, deferredToM4} {
		for name := range m {
			if _, ok := found[name]; !ok {
				bad = append(bad, fmt.Sprintf("%s is listed but no longer writes (renamed or removed): drop the entry", name))
			}
		}
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error(b)
	}
}

func TestNotYetGuardedIsEmptyOnceEnforcementIsComplete(t *testing.T) {
	if enforcementComplete && len(notYetGuarded) > 0 {
		t.Errorf("enforcementComplete is set but %d writes are not guarded yet", len(notYetGuarded))
	}
}

func TestDeferredToM4IsEmptyOnceM4IsComplete(t *testing.T) {
	if m4Complete && len(deferredToM4) > 0 {
		t.Errorf("m4Complete is set but %d writes deferred to M4 are not guarded yet", len(deferredToM4))
	}
}

// sources is every non-test Go file under internal/ and cmd/ (testdata excluded).
func sources(t *testing.T) []string {
	t.Helper()
	var files []string
	for _, root := range []string{"..", filepath.Join("..", "..", "cmd")} {
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() && e.Name() == "testdata" {
				return filepath.SkipDir
			}
			if !e.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

// pkgKey names a file's package by its directory: relative to internal/ ("db", "rivian"), or
// "cmd/<name>" under cmd/.
func pkgKey(f string) string {
	d := filepath.ToSlash(filepath.Dir(f))
	if r, ok := strings.CutPrefix(d, "../../"); ok {
		return r
	}
	return strings.TrimPrefix(d, "../")
}

func mustRead(t *testing.T, f string) string {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestOwnerPackagesDoNotPersistSessions keeps the session writes off the owner's path: no package
// whose code runs as a group's owner (the monitor and recorder, the syncs, the sends, the leases)
// may import internal/secrets. A session is stored only by a sign-in, a sign-out or an admin action,
// which no lease guards. If an owner path ever has to persist a session (a token refresh, a
// persisted csrf pair), it must do it with the epoch AND a session-version CAS (single-owner design
// 4.3, DA's point 2), not by importing secrets here: that is what this test is for.
func TestOwnerPackagesDoNotPersistSessions(t *testing.T) {
	owner := []string{"rivian", "leases", "receipts", "otadetails", "engage", "push", "samples", "drives", "charges", "packhealth"}
	for _, d := range owner {
		files, err := filepath.Glob(filepath.Join("..", d, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range af.Imports {
				if strings.Trim(imp.Path.Value, `"`) == "github.com/apohor/rivolt/internal/secrets" {
					t.Errorf("%s imports internal/secrets: the owner's path must not persist a session (see the comment)", f)
				}
			}
		}
	}
}
