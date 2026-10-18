package adminhealth

import "context"

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
	// FirstData7d and FirstData28d count first_data events in the window.
	FirstData7d  int `json:"first_data_7d"`
	FirstData28d int `json:"first_data_28d"`
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
	if err := s.cfg.DB.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE ts > now() - interval '7 days'), count(*)
		  FROM events WHERE name = 'first_data' AND ts > now() - interval '28 days'`).Scan(&e.FirstData7d, &e.FirstData28d); err != nil {
		return fail("events")
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
