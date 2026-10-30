package fence_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The unguarded-write lint (single-owner design 4.3, test 9): every function in samples, drives,
// charges, rivian, engage, push, packhealth, receipts and secrets that writes must carry the fence (the
// rivolt_lease_ok guard or a fence token from the context), or be listed here with a reason.
//
// notYetGuarded is the work list of the enforcement PRs. It only shrinks: each PR that guards a
// write removes its entry, and the test fails for an entry that is guarded already, so the list
// cannot go stale. The last enforcement PR empties it and flips enforcementComplete.
// notOwnerSide is permanent: writes that are not made by the group's owner (user actions,
// imports, repairs, maintenance jobs), which no lease guards.

const enforcementComplete = false

// The guard is context-based (a fence token in ctx), so a function that owners and non-owners both
// call is guarded for the owner's calls and unguarded for the rest: it belongs in notYetGuarded
// until it takes the token, whoever else calls it.
var notYetGuarded = map[string]string{
	"samples.Store.InsertBulk":               "the monitor's replay catch-up flush (rivian/replay_catchup.go) is owner-side and flushes on lease loss; the import caller stays unguarded (no token in ctx)",
	"drives.Store.Upsert":                    "drives PR",
	"charges.Store.Upsert":                   "charges PR",
	"charges.Store.AbandonOpenLive":          "charges PR (live charge session)",
	"charges.Store.RefreshOpenLive":          "charges PR (live charge session)",
	"charges.Store.CloseStaleOpenLive":       "charges PR (live charge session)",
	"charges.Store.CloseStaleOpenLiveBefore": "charges PR (live charge session)",
	"charges.Store.ApplyNetworkRate":         "classify in the charges PR: owner close hook or user action",
	"charges.Store.ConfirmNetwork":           "classify in the charges PR: owner close hook or user action",
	"charges.Store.MarkFree":                 "classify in the charges PR: owner close hook or user action",
	"charges.Store.UpdatePricing":            "classify in the charges PR: owner close hook or user action",
	"engage.pgStore.Claim":                   "sends PR: the epoch goes into the claim insert, so a zombie cannot claim (charging complete, left-open, plug reminder, milestones); other callers, such as the statement job, have no token",
	"engage.pgStore.Mark":                    "sends PR: result of a claimed send",
	"engage.pgStore.RecordSkip":              "sends PR: a finished skip row is a claim too",
	"push.Store.CreateNotification":          "sends PR: the notification row of an owner-triggered send",
	"push.Store.RegisterActivity":            "sends PR: Live Activity start, owner-side",
	"push.Store.EndActivity":                 "sends PR: Live Activity end, owner-side",
	"push.Store.SetAPNsLive":                 "sends PR: Live Activity state, owner-side",
	"push.Store.claimActivityUpdate":         "sends PR: the Live Activity update claim",
	"packhealth.Store.Upsert":                "pack-health PR: samples written at charge end by the owner",
	"receipts.Store.claim":                   "receipts PR: the claim insert of the owner's receipts sync",
	"receipts.Store.UpsertReceipts":          "receipts PR: the owner's receipts fetch",
	"receipts.Store.SaveDetail":              "receipts PR: the owner's receipts fetch",
	"receipts.Store.Apply":                   "receipts PR: matches a receipt to a charge after the owner's fetch",
	"receipts.Store.MarkResult":              "receipts PR: the owner's sync result",
	"receipts.Store.RefreshMatched":          "receipts PR: runs inside the owner's syncer (syncer.go)",
	"secrets.Store.Put":                      "session PR: the owner's persist (csrf heal, refresh) takes epoch AND version CAS; login keeps the plain write (a new session, version only)",
	"secrets.Store.PutTx":                    "session PR: as Put, inside the session transaction",
	"secrets.setSignedIn":                    "session PR: written in the session's transaction",
}

// notOwnerSide is permanent. Each entry names who calls it.
var notOwnerSide = map[string]string{
	"samples.Store.Reset":                  "user action (reset data)",
	"samples.Thinner.thin":                 "singleton maintenance job",
	"samples.Thinner.dropExpiredFull":      "singleton maintenance job",
	"samples.Thinner.rethinPair":           "singleton maintenance job",
	"drives.Store.Reset":                   "user action (reset data)",
	"drives.Store.SetTrailerOverride":      "user action",
	"charges.Store.Reset":                  "user action (reset data)",
	"charges.Store.DeleteByExternalID":     "user action / import",
	"charges.Store.RestampPlace":           "user action",
	"charges.Store.UndoRestamp":            "user action",
	"charges.Store.SetLocationOverride":    "user action",
	"charges.Store.SetPlaceOverride":       "user action",
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
	"packhealth.Store.BackfillAll":         "one-off backfill job (cmd/rivolt/main.go)",
	"receipts.Store.purge":                 "user action and retention job (DeleteForUser / opt-out purge)",
	"secrets.Store.Delete":                 "user action (logout, delete, disconnect)",
	"secrets.Store.DeleteTx":               "user action (logout, delete, disconnect)",
	"secrets.RivianSessionLister.backfill": "one-time sign-in time backfill",
}

// Not SQL, so not in this lint: the Redis live-state writes (the epoch CAS PR) and the Rivian calls
// (the deadline-context PR) are tracked as their own enforcement steps.

var writeSQL = regexp.MustCompile(`(?is)\b(INSERT\s+INTO\s+[a-z_]+|UPDATE\s+[a-z_]+(\s+[a-z_]+)?\s+SET\b|DELETE\s+FROM\s+[a-z_]+)`)

// guarded reports whether a function body carries the fence.
func guarded(src string) bool {
	return strings.Contains(src, "rivolt_lease_ok") || strings.Contains(src, "fence.From(") || strings.Contains(src, "guardedInsert")
}

// writers returns "pkg.Recv.Func" for every function in the scanned files whose string literals
// contain write SQL, and whether it is guarded.
func writers(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	dirs := []string{"samples", "drives", "charges", "rivian", "engage", "push", "packhealth", "receipts", "secrets"}
	for _, d := range dirs {
		files, err := filepath.Glob(filepath.Join("..", d, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			base := filepath.Base(f)
			if strings.HasSuffix(base, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			af, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
			if err != nil {
				t.Fatal(err)
			}
			src := mustRead(t, f)
			for _, decl := range af.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				writes := false
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if bl, ok := n.(*ast.BasicLit); ok && bl.Kind == token.STRING && writeSQL.MatchString(bl.Value) {
						writes = true
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
				body := src[fset.Position(fd.Pos()).Offset:fset.Position(fd.End()).Offset]
				out[d+"."+name] = guarded(body)
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
		switch {
		case isGuarded && pending:
			bad = append(bad, fmt.Sprintf("%s is guarded now: remove it from notYetGuarded", name))
		case isGuarded && exempt:
			bad = append(bad, fmt.Sprintf("%s is guarded: it does not belong in notOwnerSide", name))
		case !isGuarded && !pending && !exempt:
			bad = append(bad, fmt.Sprintf("%s writes without the fence: guard it (rivolt_lease_ok in the statement, token from fence.From), or list it with a reason", name))
		}
	}
	for _, m := range []map[string]string{notYetGuarded, notOwnerSide} {
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

func mustRead(t *testing.T, f string) string {
	t.Helper()
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
