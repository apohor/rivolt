package leases

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

// Group leases (M1 of the single-owner design). A user group (users linked
// through a shared Rivian id, see userGroups) gets one account_leases row, held by
// the pod that holds every vehicle lease of the group. Nothing decides anything
// from the row yet: it is written next to the vehicle leases so M2 can switch to
// it, and the fence token each pod keeps is only checked and counted.

// GroupToken is the proof a pod holds a group: the lease's epoch changes on every
// acquire or steal and never on a renew.
type GroupToken struct {
	Group string
	Epoch int64
}

// GroupResult is what one RenewGroups pass saw.
type GroupResult struct {
	// Held is the number of groups this pod now holds.
	Held int
	// DryRunRejected lists groups whose token from the previous pass would have
	// been refused by rivolt_lease_ok now (the lease expired or moved under us).
	DryRunRejected []string
}

// Generation is the pod's template generation (its pod-template-hash), read from
// RIVOLT_POD_GENERATION; empty when unset. M2's rollout rules read it.
func generation() string { return os.Getenv("RIVOLT_POD_GENERATION") }

// RenewGroups writes this pod's group lease rows and registers the pod. It is
// called once per reconcile, after the vehicle leases are renewed and acquired.
//
//  1. Check the tokens held last pass (dry run: log and count, nothing acts).
//  2. Upsert a row for every group whose vehicle leases are all mine and
//     unexpired. A row this pod already holds and that has not expired keeps its
//     epoch; any other acquire or steal takes a new one. A row held by another pod
//     is taken only once it has expired.
//  3. Release this pod's rows for groups it no longer fully holds (pod_id NULL, released_at
//     set; the row stays so M2 can tell how long a group has been free).
//
// A group whose users have a stored session but no vehicle gets no row in M1; M2 widens the
// candidates. Per tick that is 3 statements (pod registration, one query for the tokens held last
// pass, the upsert) plus one release UPDATE, all on the system pool's existing connections.
func (s *Store) RenewGroups(ctx context.Context) (GroupResult, error) {
	var res GroupResult
	sentAt := time.Now() // the monotonic clock: the local lease deadline counts from when the renew was SENT
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO lease_pods (pod_id, generation, started_at, seen_at)
		VALUES ($1, $2, now(), now())
		ON CONFLICT (pod_id) DO UPDATE SET generation = EXCLUDED.generation, seen_at = now()`,
		s.podID, generation()); err != nil {
		return res, fmt.Errorf("lease pod register: %w", err)
	}

	// Pod names change on every rollout; drop the rows of pods not seen for a day so they do not
	// pile up (and so a fair-share rule never counts a dead pod).
	if _, err := s.db.ExecContext(ctx, `DELETE FROM lease_pods WHERE pod_id <> $1 AND seen_at < now() - interval '1 day'`, s.podID); err != nil {
		return res, fmt.Errorf("lease pod prune: %w", err)
	}

	s.groupMu.Lock()
	prev := make([]GroupToken, 0, len(s.groups))
	for g, e := range s.groups {
		prev = append(prev, GroupToken{Group: g, Epoch: e})
	}
	s.groupMu.Unlock()
	sort.Slice(prev, func(i, j int) bool { return prev[i].Group < prev[j].Group })
	if len(prev) > 0 {
		gs := make([]string, len(prev))
		es := make([]int64, len(prev))
		for i, t := range prev {
			gs[i], es[i] = t.Group, t.Epoch
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT t.g FROM unnest($1::text[], $2::bigint[]) AS t(g, e)
			 WHERE NOT rivolt_lease_ok(t.g::uuid, $3, t.e)`, gs, es, s.podID)
		if err != nil {
			return res, fmt.Errorf("lease fence dry run: %w", err)
		}
		for rows.Next() {
			var g string
			if err := rows.Scan(&g); err != nil {
				_ = rows.Close()
				return res, err
			}
			res.DryRunRejected = append(res.DryRunRejected, g)
		}
		if err := rows.Close(); err != nil {
			return res, err
		}
	}

	rows, err := s.db.QueryContext(ctx, userGroups+`,
	gv AS (
	    SELECT DISTINCT g.gid, v.rivian_vehicle_id AS vid
	      FROM vehicles v JOIN grp g ON g.user_id = v.user_id
	     WHERE v.rivian_vehicle_id <> '' AND v.rivian_vehicle_id NOT LIKE 'electrafi-%'
	), held AS (
	    SELECT gv.gid
	      FROM gv LEFT JOIN subscription_leases l
	        ON l.vehicle_id = gv.vid AND l.pod_id = $1 AND l.expires_at > now()
	     GROUP BY gv.gid
	    HAVING bool_and(l.vehicle_id IS NOT NULL)
	)
	-- nextval is evaluated for every candidate row, including ones the upsert then leaves alone, so
	-- a renew burns a sequence value. Harmless for a bigint; only the ordering matters.
	INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
	SELECT held.gid::uuid, $1, nextval('account_lease_epoch'), now(), now(), now() + $2::interval FROM held
	 -- A group is keyed by its smallest user id, so a user who joins it (a shared vehicle) with a
	 -- smaller id re-keys it. While another pod still holds a live row keyed by any other member of
	 -- the group, the re-keyed row is not taken: two rows must never both be fence-valid for
	 -- overlapping users. The old holder drops its row when it stops holding the whole group, or it
	 -- expires.
	 WHERE NOT EXISTS (
	       SELECT 1 FROM grp m JOIN account_leases o ON o.group_id::text = m.user_id::text
	        WHERE m.gid = held.gid AND o.group_id::text <> held.gid
	          AND o.pod_id IS NOT NULL AND o.pod_id <> $1 AND o.expires_at > now())
	ON CONFLICT (group_id) DO UPDATE SET
	    pod_id      = EXCLUDED.pod_id,
	    epoch       = CASE WHEN account_leases.pod_id = EXCLUDED.pod_id AND account_leases.expires_at > now()
	                       THEN account_leases.epoch ELSE EXCLUDED.epoch END,
	    acquired_at = CASE WHEN account_leases.pod_id = EXCLUDED.pod_id AND account_leases.expires_at > now()
	                       THEN account_leases.acquired_at ELSE EXCLUDED.acquired_at END,
	    renewed_at  = EXCLUDED.renewed_at,
	    expires_at  = EXCLUDED.expires_at,
	    released_at = NULL
	WHERE account_leases.pod_id = EXCLUDED.pod_id OR account_leases.pod_id IS NULL OR account_leases.expires_at < now()
	RETURNING group_id::text, epoch`, s.podID, fmt.Sprintf("%d seconds", int(s.ttl.Seconds())))
	if err != nil {
		return res, fmt.Errorf("lease groups: %w", err)
	}
	now := map[string]int64{}
	for rows.Next() {
		var g string
		var e int64
		if err := rows.Scan(&g, &e); err != nil {
			_ = rows.Close()
			return res, err
		}
		now[g] = e
	}
	if err := rows.Close(); err != nil {
		return res, err
	}

	keep := make([]string, 0, len(now))
	for g := range now {
		keep = append(keep, g)
	}
	sort.Strings(keep)
	if _, err := s.db.ExecContext(ctx,
		`UPDATE account_leases SET pod_id = NULL, released_at = statement_timestamp()
		  WHERE pod_id = $1 AND group_id::text <> ALL($2::text[])`,
		s.podID, keep); err != nil {
		return res, fmt.Errorf("lease groups release: %w", err)
	}

	s.groupMu.Lock()
	s.groups = now
	s.groupOKAt = sentAt
	s.groupMu.Unlock()
	res.Held = len(now)
	return res, nil
}

// GroupTokens returns the fence tokens this pod holds, as of the last RenewGroups.
func (s *Store) GroupTokens() []GroupToken {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	out := make([]GroupToken, 0, len(s.groups))
	for g, e := range s.groups {
		out = append(out, GroupToken{Group: g, Epoch: e})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	return out
}

// releaseGroups releases this pod's group rows (pod_id NULL, released_at set) and deletes its
// pod row.
func (s *Store) releaseGroups(ctx context.Context) error {
	s.groupMu.Lock()
	s.groups = map[string]int64{}
	s.groupMu.Unlock()
	var errs []error
	if _, err := s.db.ExecContext(ctx, `UPDATE account_leases SET pod_id = NULL, released_at = statement_timestamp() WHERE pod_id = $1`, s.podID); err != nil {
		errs = append(errs, err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM lease_pods WHERE pod_id = $1`, s.podID); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// localDeadline is how long after a group renew was sent this pod may still act for its groups:
// two thirds of the TTL (20 s of 30 s once M2's timing lands), so it is always before the DB expiry
// and needs no clock comparison.
func (s *Store) localDeadline() time.Duration { return s.ttl * 2 / 3 }

// PastLocalDeadline reports whether this pod holds groups whose last successful renew was sent more
// than the local deadline ago: it would be fencing itself (M2) and its writes would be refused. It
// uses the monotonic clock and no database call. False when no group is held.
func (s *Store) PastLocalDeadline() bool {
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	return len(s.groups) > 0 && !s.groupOKAt.IsZero() && time.Since(s.groupOKAt) > s.localDeadline()
}
