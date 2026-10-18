package adminhealth

import (
	"context"
	"database/sql"
)

// Engagement is the "do people come back?" readout: counts only, read from rows
// the product already keeps (sessions, vehicles, drives, charges, the sent-mail
// columns and the events table). No per-account list leaves this section.
type Engagement struct {
	Section
	// Active7d and Active28d count accounts with a session seen in the window
	// (sessions.last_seen_at): the owner looked, not just the truck sending.
	Active7d  int `json:"active_7d"`
	Active28d int `json:"active_28d"`
	// ConnectedNoData counts accounts that have a vehicle but no state sample,
	// drive or charge yet.
	ConnectedNoData int `json:"connected_no_data"`
	// FirstData7d and FirstData28d count accounts whose first closed drive or
	// charge fell in the window, derived from the drives and charges tables
	// (nothing is written for it). MedianSecondsToFirstData is the median gap
	// from the account's first vehicle row to that first drive or charge, over
	// the 28-day set; null when the set is empty. An aggregate only.
	FirstData7d              int  `json:"first_data_7d"`
	FirstData28d             int  `json:"first_data_28d"`
	MedianSecondsToFirstData *int `json:"median_seconds_to_first_data"`
	// EmailsSent counts mails by kind: welcome and statement in the last 28
	// days; setup_first and setup_followup over all time (the account's
	// reminder counter).
	EmailsSent map[string]int `json:"emails_sent"`
}

func (s *Service) engagement(ctx context.Context) Engagement {
	if s.cfg.DB == nil {
		return Engagement{Section: unavailable("database not available")}
	}
	e := Engagement{EmailsSent: map[string]int{}}
	fail := func(what string) Engagement {
		return Engagement{Section: unavailable(what + " query failed")}
	}
	if err := s.cfg.DB.QueryRowContext(ctx, `
		SELECT count(DISTINCT s.user_id) FILTER (WHERE s.last_seen_at > now() - interval '7 days'),
		       count(DISTINCT s.user_id) FILTER (WHERE s.last_seen_at > now() - interval '28 days')
		  FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.last_seen_at > now() - interval '28 days' AND NOT u.disabled`).Scan(&e.Active7d, &e.Active28d); err != nil {
		return fail("sessions")
	}
	if err := s.cfg.DB.QueryRowContext(ctx, `
		SELECT count(*) FROM users u
		 WHERE NOT u.disabled
		   AND EXISTS (SELECT 1 FROM vehicles v WHERE v.user_id = u.id AND v.rivian_vehicle_id <> '' AND v.rivian_vehicle_id NOT LIKE 'electrafi-%')
		   AND NOT EXISTS (SELECT 1 FROM vehicles v JOIN vehicle_state vs ON vs.vehicle_id = v.id WHERE v.user_id = u.id)
		   AND NOT EXISTS (SELECT 1 FROM drives dr WHERE dr.user_id = u.id)
		   AND NOT EXISTS (SELECT 1 FROM charges ch WHERE ch.user_id = u.id)`).Scan(&e.ConnectedNoData); err != nil {
		return fail("connected")
	}
	var median sql.NullFloat64
	if err := s.cfg.DB.QueryRowContext(ctx, `
		WITH first AS (
		  SELECT x.user_id, MIN(x.t) AS first_at FROM (
		    SELECT user_id, MIN(ended_at) AS t FROM drives   WHERE ended_at IS NOT NULL GROUP BY user_id
		    UNION ALL
		    SELECT user_id, MIN(ended_at) AS t FROM charges  WHERE ended_at IS NOT NULL GROUP BY user_id
		  ) x GROUP BY x.user_id
		)
		SELECT count(*) FILTER (WHERE f.first_at > now() - interval '7 days'),
		       count(*) FILTER (WHERE f.first_at > now() - interval '28 days'),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY GREATEST(0, EXTRACT(EPOCH FROM f.first_at - v.mc)))
		         FILTER (WHERE f.first_at > now() - interval '28 days' AND v.mc IS NOT NULL)
		  FROM first f
		  JOIN users u ON u.id = f.user_id AND NOT u.disabled
		  LEFT JOIN (SELECT user_id, MIN(created_at) AS mc FROM vehicles GROUP BY user_id) v ON v.user_id = f.user_id`).
		Scan(&e.FirstData7d, &e.FirstData28d, &median); err != nil {
		return fail("first data")
	}
	if median.Valid {
		m := int(median.Float64)
		e.MedianSecondsToFirstData = &m
	}
	var welcome, first, follow, statement int
	// The welcome column was back-filled on every existing account when it was
	// added, so only a send within two days of signup is a real one.
	if err := s.cfg.DB.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE welcome_sent_at > now() - interval '28 days' AND welcome_sent_at < created_at + interval '2 days'),
		       count(*) FILTER (WHERE setup_nudge_count >= 1),
		       count(*) FILTER (WHERE setup_nudge_count >= 2)
		  FROM users`).Scan(&welcome, &first, &follow); err != nil {
		return fail("mail")
	}
	if err := s.cfg.DB.QueryRowContext(ctx, `
		SELECT count(*) FROM statement_email_sends WHERE status = 'sent' AND sent_at > now() - interval '28 days'`).Scan(&statement); err != nil {
		return fail("statement mail")
	}
	e.EmailsSent["welcome"], e.EmailsSent["setup_first"], e.EmailsSent["setup_followup"], e.EmailsSent["statement"] = welcome, first, follow, statement
	e.Section = Section{Status: OK}
	return e
}
