package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// rivolt open-charges snapshot|verify: the release roll's open-charge check as two short Jobs (release gitops, PR 5). It is the old releasectl
// open_charge_gate / open_charge_verify, run inside the cluster with a database role that can read five columns of charges and nothing else.
//
//	snapshot  lists the charges open now (charging_active, last sample under 10 minutes old) and writes them as JSON to stdout, to --out FILE,
//	          or to a ConfigMap (--configmap NAME, through the pod's service account). Exits 0 with an empty list.
//	verify    reads that JSON (--snapshot FILE, the ConfigMap mounted as a volume) after the roll and checks each charge is still exactly one row:
//	          same id, no second row for the vehicle since it started, and while still charging_active a last sample under 10 minutes old.
//	          Exits 1 for a charge that split, vanished or stalled, or when the database can't be read. A charge that ended meanwhile passes; one
//	          that ended inside the roll window (--roll-from EPOCH, or env ROLL_FROM) also prints a WARN, since a lost stream closes charges.
//
// The database is RIVOLT_VERIFY_DATABASE_URL (the read-only role), never the app's. Unknown arguments exit 2.
const (
	openChargeWindow  = 10 * time.Minute
	openChargesKey    = "open-charges.json"
	verifyStmtTimeout = "5s"
)

type openCharge struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
}

type openChargeSnapshot struct {
	TakenAt time.Time    `json:"taken_at"`
	Charges []openCharge `json:"charges"`
}

func runOpenCharges(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "snapshot" && args[0] != "verify") {
		fmt.Fprintln(stderr, "usage: rivolt open-charges snapshot [--out FILE] [--configmap NAME [--namespace NS]] | verify --snapshot FILE [--roll-from EPOCH]")
		return 2
	}
	fs := flag.NewFlagSet("open-charges "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "snapshot: write the JSON to this file (default: stdout)")
	cm := fs.String("configmap", "", "snapshot: write the JSON to this ConfigMap, key "+openChargesKey)
	ns := fs.String("namespace", "", "snapshot: the ConfigMap's namespace (default: the pod's own)")
	snap := fs.String("snapshot", "", "verify: the snapshot JSON file")
	rollFrom := fs.String("roll-from", os.Getenv("ROLL_FROM"), "verify: epoch seconds the roll began (default env ROLL_FROM)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "open-charges: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	dsn := strings.TrimSpace(os.Getenv("RIVOLT_VERIFY_DATABASE_URL"))
	if dsn == "" {
		fmt.Fprintln(stderr, "open-charges: RIVOLT_VERIFY_DATABASE_URL is not set")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// The timeout is a connection parameter, so it holds on every connection; one connection keeps the role under its CONNECTION LIMIT.
	pool, err := db.OpenNoMigrateWith(ctx, dsn, map[string]string{"statement_timeout": verifyStmtTimeout})
	if err != nil {
		fmt.Fprintln(stderr, "open-charges: postgres:", err)
		return 1
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	if args[0] == "snapshot" {
		return snapshotOpenCharges(ctx, pool, *out, *cm, *ns, stdout, stderr)
	}
	if *snap == "" {
		fmt.Fprintln(stderr, "open-charges verify: --snapshot FILE is required")
		return 2
	}
	var rf int64
	if *rollFrom != "" {
		if rf, err = strconv.ParseInt(*rollFrom, 10, 64); err != nil {
			fmt.Fprintln(stderr, "open-charges verify: --roll-from must be epoch seconds")
			return 2
		}
	}
	return verifyOpenCharges(ctx, pool, *snap, rf, stdout, stderr)
}

func listOpenCharges(ctx context.Context, q *sql.DB) ([]openCharge, error) {
	rows, err := q.QueryContext(ctx, `SELECT id::text, started_at FROM charges
		WHERE final_state LIKE 'charging\_active%' AND ended_at > now() - interval '10 minutes' ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var list []openCharge
	for rows.Next() {
		var c openCharge
		if err := rows.Scan(&c.ID, &c.StartedAt); err != nil {
			return nil, err
		}
		list = append(list, c)
	}
	return list, rows.Err()
}

func snapshotOpenCharges(ctx context.Context, q *sql.DB, outFile, cmName, ns string, stdout, stderr io.Writer) int {
	list, err := listOpenCharges(ctx, q)
	if err != nil {
		fmt.Fprintln(stderr, "open-charges snapshot: can't read the open charges:", err)
		return 1
	}
	if list == nil {
		list = []openCharge{}
	}
	body, _ := json.Marshal(openChargeSnapshot{TakenAt: time.Now().UTC(), Charges: list})
	switch {
	case cmName != "":
		kc, err := inClusterKube(ns)
		if err != nil {
			fmt.Fprintln(stderr, "open-charges snapshot:", err)
			return 1
		}
		if err := kc.putConfigMap(ctx, cmName, map[string]string{openChargesKey: string(body)}); err != nil {
			fmt.Fprintln(stderr, "open-charges snapshot: configmap:", err)
			return 1
		}
	case outFile != "":
		if err := os.WriteFile(outFile, append(body, '\n'), 0o600); err != nil {
			fmt.Fprintln(stderr, "open-charges snapshot:", err)
			return 1
		}
	default:
		fmt.Fprintln(stdout, string(body))
	}
	fmt.Fprintf(stderr, "open charges: %d\n", len(list))
	return 0
}

// chargeNow is what the database says about a charge that was open before the roll.
type chargeNow struct {
	Missing    bool
	Rows       int    // rows for the vehicle that started within a minute of the snapshot's start, or later
	RowIDs     string // their short ids
	State      string
	AgeSeconds int64 // since the charge's last sample
	NoEnd      bool  // the row has no end time, so there is no last sample to age
}

// classifyOpenCharge is the old open_charge_verify's rule set: level is "ok", "warn" or "fail".
func classifyOpenCharge(id string, n chargeNow, sinceRoll int64) (level, msg string) {
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	active := strings.HasPrefix(n.State, "charging_active")
	switch {
	case n.Missing:
		return "fail", fmt.Sprintf("FAIL open charge %s: its row is gone after the roll", short)
	case n.NoEnd:
		return "fail", fmt.Sprintf("FAIL open charge %s: the row has no end time, so its last sample can't be checked", short)
	case n.Rows != 1:
		return "fail", fmt.Sprintf("FAIL open charge %s: %d rows for the vehicle since it started (it split, or a new charge began): %s", short, n.Rows, strings.ReplaceAll(n.RowIDs, ",", ", "))
	case active && n.AgeSeconds > int64(openChargeWindow/time.Second):
		return "fail", fmt.Sprintf("FAIL open charge %s: still charging_active but its last sample is %ds old (stalled)", short, n.AgeSeconds)
	case !active && n.AgeSeconds <= sinceRoll:
		return "warn", fmt.Sprintf("WARN open charge %s: ended as %s %ds ago, inside the roll window (the roll began %ds ago): check it was a real unplug, not a lost stream", short, n.State, n.AgeSeconds, sinceRoll)
	}
	return "ok", fmt.Sprintf("open charge %s: one row, %s, last sample %ds ago", short, n.State, n.AgeSeconds)
}

func readChargeNow(ctx context.Context, q *sql.DB, c openCharge) (chargeNow, error) {
	var n chargeNow
	if err := q.QueryRowContext(ctx, `SELECT count(*), coalesce(string_agg(substr(id::text, 1, 8), ','), '') FROM charges
		WHERE vehicle_id = (SELECT vehicle_id FROM charges WHERE id = $1::uuid) AND started_at >= $2::timestamptz - interval '1 minute'`, c.ID, c.StartedAt).Scan(&n.Rows, &n.RowIDs); err != nil {
		return n, err
	}
	var state sql.NullString
	var age sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT final_state, extract(epoch FROM now() - ended_at)::bigint FROM charges WHERE id = $1::uuid`, c.ID).Scan(&state, &age)
	if errors.Is(err, sql.ErrNoRows) {
		n.Missing = true
		return n, nil
	}
	n.State, n.AgeSeconds, n.NoEnd = state.String, age.Int64, !age.Valid
	return n, err
}

func verifyOpenCharges(ctx context.Context, q *sql.DB, snapFile string, rollFrom int64, stdout, stderr io.Writer) int {
	raw, err := os.ReadFile(snapFile)
	if err != nil {
		fmt.Fprintln(stderr, "FAIL open charges: can't read the snapshot:", err)
		return 1
	}
	var snap openChargeSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		fmt.Fprintln(stderr, "FAIL open charges: the snapshot is not valid:", err)
		return 1
	}
	if len(snap.Charges) == 0 {
		fmt.Fprintln(stdout, "open charges: none were open before the roll; nothing to verify")
		return 0
	}
	var since int64
	if rollFrom > 0 {
		since = time.Now().Unix() - rollFrom
	}
	bad := 0
	for _, c := range snap.Charges {
		n, err := readChargeNow(ctx, q, c)
		if err != nil {
			short := c.ID
			if len(short) > 8 {
				short = short[:8]
			}
			fmt.Fprintf(stdout, "FAIL open charge %s: can't read the database to verify it after the roll: %v\n", short, err)
			bad = 1
			continue
		}
		level, msg := classifyOpenCharge(c.ID, n, since)
		fmt.Fprintln(stdout, msg)
		if level == "fail" {
			bad = 1
		}
	}
	return bad
}

// kubeAPI is the few calls the snapshot needs from the Kubernetes API, with the pod's service account.
type kubeAPI struct {
	base, token, ns string
	hc              *http.Client
}

const saDir = "/var/run/secrets/kubernetes.io/serviceaccount"

func inClusterKube(ns string) (*kubeAPI, error) {
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return nil, errors.New("not in a cluster (KUBERNETES_SERVICE_HOST is empty); use --out instead")
	}
	tok, err := os.ReadFile(saDir + "/token")
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("service account CA is not valid")
	}
	if ns == "" {
		b, err := os.ReadFile(saDir + "/namespace")
		if err != nil {
			return nil, err
		}
		ns = strings.TrimSpace(string(b))
	}
	return &kubeAPI{
		base: "https://" + host + ":" + port, token: strings.TrimSpace(string(tok)), ns: ns,
		hc: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}},
	}, nil
}

// putConfigMap creates the ConfigMap, or replaces its data when it exists.
func (k *kubeAPI) putConfigMap(ctx context.Context, name string, data map[string]string) error {
	body, _ := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"name": name, "namespace": k.ns},
		"data":     data,
	})
	do := func(method, url string) (int, string, error) {
		req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
		if err != nil {
			return 0, "", err
		}
		req.Header.Set("Authorization", "Bearer "+k.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := k.hc.Do(req)
		if err != nil {
			return 0, "", err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return resp.StatusCode, string(b), nil
	}
	coll := k.base + "/api/v1/namespaces/" + k.ns + "/configmaps"
	code, msg, err := do(http.MethodPost, coll)
	if err != nil {
		return err
	}
	if code == http.StatusConflict {
		code, msg, err = do(http.MethodPut, coll+"/"+name)
		if err != nil {
			return err
		}
	}
	if code < 200 || code > 299 {
		return fmt.Errorf("kubernetes API answered %d: %s", code, msg)
	}
	return nil
}
