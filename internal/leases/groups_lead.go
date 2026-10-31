package leases

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

// Leading with the group lease (M2 of the single-owner design). With lead mode on, a pod
// acquires a user group first (one account_leases row, a fresh epoch) and only then the
// vehicle leases of that group, so the group row decides who owns what and the pods can be
// spread: each pod takes at most its fair share of the groups. Vehicle rows are still written,
// so a pod running the M1 code mid-rollout still sees them as held.

const (
	// releasedGrace is how long a released group stays free for the newest generation only; an
	// older generation may take it after this.
	releasedGrace = 10 * time.Second
	// maxGroupsPerTick caps the groups one pod acquires in one pass.
	maxGroupsPerTick = 20
)

// GroupLead is what one lead-mode pass decided.
type GroupLead struct {
	// Held is the groups this pod holds after the pass, with their epochs.
	Held map[string]int64
	// Lost are groups held last pass that are no longer ours (expired and taken, or no longer a
	// candidate). Their subscriptions must stop; the row is not ours to release.
	Lost []string
	// Shed are groups held beyond the fair share (one per pass). The caller stops them and then
	// calls ReleaseGroup: stop first, release second.
	Shed []string
	// ShedOrder is every held group in the order a shed takes them (highest id first), set when
	// the pod is over its share; Shed is its first. A coordinator that knows which groups are
	// busy (an open drive or charge) picks the first idle one instead (Coordinator.SetActivityCheck).
	ShedOrder []string
	// Retire are held rows that are no longer candidates (the session was removed, or the group was
	// re-keyed). Same rule as Shed: stop first, then ReleaseGroup. They are also in Lost.
	Retire []string
}

// Lead mode timing (design 4.1): a 30 s lease renewed every 5 s (+-1 s of jitter), a 3 s
// statement timeout on the renew, and a local deadline of 20 s after the renew was sent.
const (
	GroupTTL          = 30 * time.Second
	groupTick         = 5 * time.Second
	groupTickJitter   = time.Second
	groupRenewTimeout = 3 * time.Second
	// groupPassBudget bounds a whole lead tick (renew, acquire, stops), under the 5 s tick.
	groupPassBudget = 3500 * time.Millisecond
)

// SetGroupLead turns lead mode on or off. On, the lease TTL is 30 s and the coordinator ticks
// every 5 s; call it before NewCoordinator.
func (s *Store) SetGroupLead(on bool) {
	s.groupMu.Lock()
	s.lead = on
	if on {
		s.ttl = GroupTTL
	} else {
		s.ttl = DefaultTTL
	}
	s.groupMu.Unlock()
}

// tickInterval is how often the coordinator reconciles: TTL/4, or 5 s in lead mode.
func (s *Store) tickInterval() time.Duration {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	if s.lead {
		return groupTick
	}
	return s.ttl / 4
}

// DropGroups forgets every group token (the pod has stopped acting for them). The rows expire
// on their own.
func (s *Store) DropGroups() {
	s.groupMu.Lock()
	s.groups = map[string]int64{}
	s.groupMu.Unlock()
}

// GroupLeading reports whether lead mode is on.
func (s *Store) GroupLeading() bool {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	return s.lead
}

// MarkDraining records that this pod is shutting down: it acquires nothing more and is not
// counted when the fair share is worked out. A draining pod keeps renewing what it holds until
// it releases.
func (s *Store) MarkDraining(ctx context.Context) error {
	s.groupMu.Lock()
	s.draining = true
	s.groupMu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE lease_pods SET draining = true WHERE pod_id = $1`, s.podID)
	return err
}

func (s *Store) isDraining() bool {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	return s.draining
}

// podLiveWindow is how recent a pod's lease_pods.seen_at must be for it to count as live: two
// reconcile ticks (the tick is TTL/4), so a pod that ticks on schedule is always seen by its peers.
// 60 s today, 15 s once the TTL is 30 s.
func (s *Store) podLiveWindow() time.Duration { return s.ttl / 2 }

// fairShare is ceil(groups / pods) + 1: the +1 keeps a pod from flapping on a group that
// would otherwise move back and forth.
func fairShare(groups, pods int) int {
	if pods < 1 {
		pods = 1
	}
	return (groups+pods-1)/pods + 1
}

type livePod struct {
	id         string
	generation string
	startedAt  time.Time
	draining   bool
}

// newestGeneration is the generation whose earliest started_at among the live, non-draining pods
// is the latest. Empty when there is no such pod.
func newestGeneration(pods []livePod) string {
	first := map[string]time.Time{}
	for _, p := range pods {
		if p.draining {
			continue
		}
		if t, ok := first[p.generation]; !ok || p.startedAt.Before(t) {
			first[p.generation] = p.startedAt
		}
	}
	best, bestT := "", time.Time{}
	for g, t := range first {
		if t.After(bestT) || (t.Equal(bestT) && g > best) {
			best, bestT = g, t
		}
	}
	return best
}

func (s *Store) livePods(ctx context.Context) ([]livePod, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT pod_id, generation, started_at, draining FROM lease_pods
		 WHERE seen_at > now() - $1::interval`, fmt.Sprintf("%d seconds", int(s.podLiveWindow().Seconds())))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []livePod
	for rows.Next() {
		var p livePod
		if err := rows.Scan(&p.id, &p.generation, &p.startedAt, &p.draining); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// candidateGroups labels groups that have a stored Rivian session on a member, as an extra CTE
// after userGroups.
const candidateGroups = `,
	cand AS (
	    SELECT DISTINCT g.gid FROM grp g
	     WHERE EXISTS (SELECT 1 FROM user_secrets us WHERE us.user_id = g.user_id AND us.name = 'rivian.session')
	)`

// LeadGroups is one lead-mode pass: register, renew the groups this pod holds, acquire free ones
// up to the fair share, and report what to shed. Statements per pass: pod register, prune,
// live pods, candidate count, renew, acquire, release of non-candidates. No new connection.
func (s *Store) LeadGroups(ctx context.Context) (GroupLead, error) {
	var res GroupLead
	sentAt := time.Now()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO lease_pods (pod_id, generation, started_at, seen_at, draining)
		VALUES ($1, $2, now(), now(), $3)
		ON CONFLICT (pod_id) DO UPDATE SET generation = EXCLUDED.generation, seen_at = now(), draining = EXCLUDED.draining`,
		s.podID, generation(), s.isDraining()); err != nil {
		return res, fmt.Errorf("lease pod register: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM lease_pods WHERE pod_id <> $1 AND seen_at < now() - interval '1 day'`, s.podID); err != nil {
		return res, fmt.Errorf("lease pod prune: %w", err)
	}
	pods, err := s.livePods(ctx)
	if err != nil {
		return res, fmt.Errorf("lease pods: %w", err)
	}
	nLive := 0
	var me livePod
	for _, p := range pods {
		if p.id == s.podID {
			me = p
		}
		if !p.draining {
			nLive++
		}
	}
	newest := newestGeneration(pods)
	isNewest := me.generation == newest || newest == ""

	var total int
	if err := s.db.QueryRowContext(ctx, userGroups+candidateGroups+`SELECT count(*) FROM cand`).Scan(&total); err != nil {
		return res, fmt.Errorf("lease candidates: %w", err)
	}
	share := fairShare(total, nLive)

	ttl := fmt.Sprintf("%d seconds", int(s.ttl.Seconds()))
	held, err := s.queryEpochs(ctx, userGroups+candidateGroups+`
		UPDATE account_leases SET renewed_at = now(), expires_at = now() + $2::interval
		 WHERE pod_id = $1 AND expires_at > now() AND group_id::text IN (SELECT gid FROM cand)
		RETURNING group_id::text, epoch`, s.podID, ttl)
	if err != nil {
		return res, fmt.Errorf("lease renew: %w", err)
	}

	if want := share - len(held); want > 0 && !s.isDraining() {
		if want > maxGroupsPerTick {
			want = maxGroupsPerTick
		}
		got, stolen, err := s.queryEpochsStolen(ctx, userGroups+candidateGroups+`,
			prev AS (SELECT group_id, (pod_id IS NOT NULL) AS was_held FROM account_leases),
			ins AS (
			INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
			SELECT c.gid::uuid, $1, nextval('account_lease_epoch'), now(), now(), now() + $2::interval
			  FROM cand c LEFT JOIN account_leases a ON a.group_id::text = c.gid
			 WHERE (a.group_id IS NULL
			        OR a.expires_at < now()
			        OR (a.pod_id IS NULL AND ($3 OR COALESCE(a.released_at, '-infinity') < now() - $5::interval)))
			   -- the re-key guard of the M1 mirror: another pod's live row keyed by a member
			   AND NOT EXISTS (
			       SELECT 1 FROM grp m JOIN account_leases o ON o.group_id::text = m.user_id::text
			        WHERE m.gid = c.gid AND o.group_id::text <> c.gid
			          AND o.pod_id IS NOT NULL AND o.pod_id <> $1 AND o.expires_at > now())
			 ORDER BY (a.group_id IS NULL), c.gid
			 LIMIT $4
			ON CONFLICT (group_id) DO UPDATE SET
			    pod_id = EXCLUDED.pod_id, epoch = EXCLUDED.epoch, acquired_at = EXCLUDED.acquired_at,
			    renewed_at = EXCLUDED.renewed_at, expires_at = EXCLUDED.expires_at, released_at = NULL
			WHERE account_leases.pod_id IS NULL OR account_leases.expires_at < now()
			RETURNING group_id, epoch)
			SELECT ins.group_id::text, ins.epoch, COALESCE(p.was_held, false) FROM ins LEFT JOIN prev p ON p.group_id = ins.group_id`,
			s.podID, ttl, isNewest, want, fmt.Sprintf("%d seconds", int(releasedGrace.Seconds())))
		if err != nil {
			return res, fmt.Errorf("lease acquire: %w", err)
		}
		s.groupMu.Lock()
		for g, e := range got {
			held[g] = e
			if stolen[g] {
				s.stolenAt[g] = time.Now()
			} else {
				delete(s.stolenAt, g)
			}
		}
		s.groupMu.Unlock()
	}

	// Rows this pod still holds that are not in the renewed set: no longer a candidate. They are not
	// released here: the caller stops their vehicles first and then calls ReleaseGroup.
	keep := make([]string, 0, len(held))
	for g := range held {
		keep = append(keep, g)
	}
	sort.Strings(keep)
	retire, err := s.db.QueryContext(ctx,
		`SELECT group_id::text FROM account_leases
		  WHERE pod_id = $1 AND expires_at > now() AND group_id::text <> ALL($2::text[])`, s.podID, keep)
	if err != nil {
		return res, fmt.Errorf("lease retire: %w", err)
	}
	for retire.Next() {
		var g string
		if err := retire.Scan(&g); err != nil {
			_ = retire.Close()
			return res, err
		}
		res.Retire = append(res.Retire, g)
	}
	if err := retire.Close(); err != nil {
		return res, err
	}

	s.groupMu.Lock()
	for g := range s.groups {
		if _, ok := held[g]; !ok {
			res.Lost = append(res.Lost, g)
		}
	}
	s.groups = held
	s.groupOKAt = sentAt
	for g := range s.stolenAt {
		if _, ok := held[g]; !ok {
			delete(s.stolenAt, g)
		}
	}
	s.groupMu.Unlock()
	res.Lost = append(res.Lost, res.Retire...)
	sort.Strings(res.Lost)
	res.Lost = dedupe(res.Lost)

	if over := len(held) - share; over > 0 {
		// One group per pass, the highest id: a fixed order so two passes never pick differently.
		for i := len(keep) - 1; i >= 0; i-- {
			res.ShedOrder = append(res.ShedOrder, keep[i])
		}
		res.Shed = res.ShedOrder[:1:1]
	}
	if s.isDraining() {
		res.Shed, res.ShedOrder = nil, nil // a draining pod releases everything at shutdown, not one by one
	}
	res.Held = held
	return res, nil
}

// AcquireGroups takes the given candidate groups when they are free (no row, expired, or released),
// outside the fair share and the newest-generation grace, and returns the ones it got with their
// epochs. The coordinator calls it for a vehicle with an open drive or charge whose group changed
// under it (re-keyed or split by a shared vehicle), so the session is not handed over mid-way; the
// next passes shed the group once it is idle. A draining pod acquires nothing. The re-key guard
// holds as in LeadGroups.
func (s *Store) AcquireGroups(ctx context.Context, groups []string) (map[string]int64, error) {
	if len(groups) == 0 || s.isDraining() {
		return map[string]int64{}, nil
	}
	ttl := fmt.Sprintf("%d seconds", int(s.ttl.Seconds()))
	got, stolen, err := s.queryEpochsStolen(ctx, userGroups+candidateGroups+`,
		prev AS (SELECT group_id, (pod_id IS NOT NULL) AS was_held FROM account_leases),
		ins AS (
		INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		SELECT c.gid::uuid, $1, nextval('account_lease_epoch'), now(), now(), now() + $2::interval
		  FROM cand c LEFT JOIN account_leases a ON a.group_id::text = c.gid
		 WHERE c.gid = ANY($3::text[])
		   AND (a.group_id IS NULL OR a.expires_at < now() OR a.pod_id IS NULL)
		   AND NOT EXISTS (
		       SELECT 1 FROM grp m JOIN account_leases o ON o.group_id::text = m.user_id::text
		        WHERE m.gid = c.gid AND o.group_id::text <> c.gid
		          AND o.pod_id IS NOT NULL AND o.pod_id <> $1 AND o.expires_at > now())
		ON CONFLICT (group_id) DO UPDATE SET
		    pod_id = EXCLUDED.pod_id, epoch = EXCLUDED.epoch, acquired_at = EXCLUDED.acquired_at,
		    renewed_at = EXCLUDED.renewed_at, expires_at = EXCLUDED.expires_at, released_at = NULL
		WHERE account_leases.pod_id IS NULL OR account_leases.expires_at < now()
		RETURNING group_id, epoch)
		SELECT ins.group_id::text, ins.epoch, COALESCE(p.was_held, false) FROM ins LEFT JOIN prev p ON p.group_id = ins.group_id`,
		s.podID, ttl, groups)
	if err != nil {
		return nil, fmt.Errorf("lease acquire for an open session: %w", err)
	}
	s.groupMu.Lock()
	for g, e := range got {
		s.groups[g] = e
		if stolen[g] {
			s.stolenAt[g] = time.Now()
		} else {
			delete(s.stolenAt, g)
		}
	}
	s.groupMu.Unlock()
	return got, nil
}

// ReleaseGroup gives one group row back (pod_id NULL, released_at set) after its subscriptions
// have stopped, and forgets its token.
func (s *Store) ReleaseGroup(ctx context.Context, group string) error {
	s.groupMu.Lock()
	delete(s.groups, group)
	s.groupMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE account_leases SET pod_id = NULL, released_at = statement_timestamp() WHERE group_id = $1::uuid AND pod_id = $2`,
		group, s.podID)
	return err
}

// GroupVehicles maps every Rivian vehicle id to the id of its user group.
func (s *Store) GroupVehicles(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, userGroups+`
		SELECT DISTINCT v.rivian_vehicle_id, g.gid FROM vehicles v JOIN grp g ON g.user_id = v.user_id
		 WHERE v.rivian_vehicle_id <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var v, g string
		if err := rows.Scan(&v, &g); err != nil {
			return nil, err
		}
		out[v] = g
	}
	return out, rows.Err()
}

func (s *Store) queryEpochs(ctx context.Context, q string, args ...any) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var g string
		var e int64
		if err := rows.Scan(&g, &e); err != nil {
			return nil, err
		}
		out[g] = e
	}
	return out, rows.Err()
}

func dedupe(in []string) []string {
	out := in[:0]
	for i, v := range in {
		if i == 0 || v != in[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// LocalDeadline is how long after a renew was sent this pod may still act: TTL - 10 s (20 s of a
// 30 s lease). Group and vehicle leases fence on this one rule.
func (s *Store) LocalDeadline() time.Duration { return s.localDeadline() }

// warmUp is how long after a steal (a group taken from a holder that did not release it) the new
// owner waits before its first guarded write: the old owner's last statement can commit up to 3 s
// after the expiry (2 s statement timeout + 1 s idle-in-transaction), so 5 s clears it.
const warmUp = 5 * time.Second

// SetVehicleGroups records which group each vehicle belongs to, as of the last lead pass (the coordinator calls it on every pass
// that read the map, before it acquires or subscribes anything). Fence and Deadline look vehicles up here: a vehicle with no entry
// reads Lost, so without this call a pod that holds every group still refuses all of its own writes. The map is copied.
func (s *Store) SetVehicleGroups(vg map[string]string) {
	cp := make(map[string]string, len(vg))
	for v, g := range vg {
		cp[v] = g
	}
	s.groupMu.Lock()
	s.vehicleGroup = cp
	s.groupMu.Unlock()
}

// Fence says what this pod may write for a vehicle (fence.Source). Lead mode off: Off. In lead
// mode a write needs a held group for the vehicle, renewed within the local deadline; a group
// stolen within the warm-up window waits.
func (s *Store) Fence(vehicleID string) (fence.Token, fence.State) {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	if !s.lead {
		return fence.Token{}, fence.Off
	}
	g, ok := s.vehicleGroup[vehicleID]
	if !ok {
		return fence.Token{}, fence.Lost
	}
	return s.tokenLocked(g)
}

// tokenLocked is the fence state of group g for this pod (caller holds groupMu, lead mode on): held
// and renewed within the local deadline, and past the warm-up if it was stolen.
func (s *Store) tokenLocked(g string) (fence.Token, fence.State) {
	e, held := s.groups[g]
	if !held || s.groupOKAt.IsZero() || time.Since(s.groupOKAt) > s.localDeadline() {
		return fence.Token{}, fence.Lost
	}
	if at, stolen := s.stolenAt[g]; stolen && time.Since(at) < warmUp {
		return fence.Token{Group: g, Pod: s.podID, Epoch: e}, fence.Warming
	}
	return fence.Token{Group: g, Pod: s.podID, Epoch: e}, fence.Held
}

// queryEpochsStolen is queryEpochs for the acquire statement, which also returns whether the row was
// held by a pod (an expired lease stolen) rather than free (released, or new).
func (s *Store) queryEpochsStolen(ctx context.Context, q string, args ...any) (map[string]int64, map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out, stolen := map[string]int64{}, map[string]bool{}
	for rows.Next() {
		var g string
		var e int64
		var was bool
		if err := rows.Scan(&g, &e, &was); err != nil {
			return nil, nil, err
		}
		out[g], stolen[g] = e, was
	}
	return out, stolen, rows.Err()
}

// Deadline is the local lease deadline for the vehicle's group: when the last renew was sent plus the
// local deadline (monotonic). ok is false with lead mode off; a vehicle whose group this pod does not
// hold has a deadline in the past.
func (s *Store) Deadline(vehicleID string) (time.Time, bool) {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	if !s.lead {
		return time.Time{}, false
	}
	g, ok := s.vehicleGroup[vehicleID]
	if _, held := s.groups[g]; !ok || !held || s.groupOKAt.IsZero() {
		return time.Unix(0, 0), true
	}
	return s.groupOKAt.Add(s.localDeadline()), true
}
