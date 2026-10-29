// Package metrics owns the application's Prometheus instrumentation.
//
// Rationale: kube-prometheus-stack already runs in the cluster and
// scrapes node/k8s/cnpg/argocd metrics out of the box. Adding a
// /metrics endpoint to rivolt with an explicit ServiceMonitor lights
// up per-handler latency, Rivian-result-class counters, and the
// other app-shaped signals the cluster-level scrapers can't see.
//
// All metrics live on a private *prometheus.Registry rather than the
// global default. Reasons:
//
//   - Tests can spin up a fresh registry per test.
//   - We don't accidentally collide with go-collector / process-collector
//     registrations from libraries that touch promauto.
//   - The handler is exported scoped to this registry, so /metrics
//     output is exactly what we asked for.
//
// Naming follows Prometheus conventions: snake_case, base unit suffix
// (_seconds, _bytes, _total).
package metrics

import (
	"database/sql"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is the rivolt-specific instrumentation surface. One
// instance is built at startup and shared with anything that
// emits a metric. Keep it small — adding a metric here means it
// shows up in Grafana, so each one should answer a real question
// you'd want to ask at 3am.
type Metrics struct {
	reg *prometheus.Registry

	// HTTP server side.
	HTTPRequestsTotal   *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec

	// Rivian upstream classification (transient/outage/rate-limited/
	// user-action/unknown — see internal/rivian/errclass.go). Counts
	// outcomes per call class, not per error string, so cardinality
	// stays bounded.
	RivianResultsTotal *prometheus.CounterVec

	// Subscription leases owned by this pod. The lease layer
	// increments/decrements as it claims and releases.
	SubscriptionLeases prometheus.Gauge
	// LeaseGroupsOwned is the number of user-group leases this pod holds (M1 of
	// the single-owner design); LeaseFenceWouldReject counts group tokens that
	// rivolt_lease_ok would have refused, with nothing acting on it yet.
	LeaseGroupsOwned      prometheus.Gauge
	LeaseFenceWouldReject *prometheus.CounterVec
	// LeaseUnstreamed counts episodes of a lease this pod holds without
	// a subscription behind it (the coordinator re-subscribes; alert on
	// any increase).
	LeaseUnstreamed prometheus.Counter
	// ReauthRaiseDeferred counts rejections whose needs_reauth raise was held back because
	// the client did not yet know which stored session it was using (the next sync decides).
	ReauthRaiseDeferred prometheus.Counter
	// RivianAccountMismatch is, per field ("session", "flag"), how many Rivian accounts loaded
	// on this pod still disagreed with the database after the last 5-second sync round
	// corrected them. Zero when the cross-pod sync works; sustained non-zero (alert: 10 min)
	// means a pod is serving state the database does not have. RivianAccountSyncErrorsTotal
	// counts rounds that could not read the database at all. No user labels.
	RivianAccountMismatch        *prometheus.GaugeVec
	RivianAccountSyncErrorsTotal prometheus.Counter
	// RivianSessionsExpiring is the number of stored Rivian sessions with at most the given
	// time left ("14d", "7d", "1d"), from the hourly expiry sweep; RivianSessionsExpired those
	// already past their expected end but not yet flagged needs_reauth (should be 0: a non-zero
	// value for long means the expiry estimate is wrong or recording stopped unnoticed).
	RivianSessionsExpiring *prometheus.GaugeVec
	RivianSessionsExpired  prometheus.Gauge
	// RivianExpiryNoticesTotal counts the expiry warnings by days left (7 or 1) and outcome.
	RivianExpiryNoticesTotal *prometheus.CounterVec

	// Circuit breaker telemetry. State is a 0/1/2 gauge
	// (closed/half_open/open) so Grafana can alert on
	// max_over_time != 0. Trips is a counter labelled by reason
	// ("rate_limited" | "outage") so we can answer "what's making
	// it open" in one PromQL.
	RivianBreakerState prometheus.Gauge
	RivianBreakerTrips *prometheus.CounterVec
	// Global token bucket gating. Counter increments every time
	// the bucket says no, partitioned by class so we can answer
	// "is priority being starved".
	RivianRateLimitBlocked *prometheus.CounterVec
	// RivianBudgetFallbackTotal counts outbound Rivian calls gated by the per-process bucket
	// because Redis could not serve the global one (by class). A sustained rate means the
	// global budget is not being enforced across pods.
	RivianBudgetFallbackTotal *prometheus.CounterVec
	// PlanQuotaFallbackTotal counts planner-quota checks counted in the process because Redis
	// could not serve them (or there is none): the quota is then per pod.
	PlanQuotaFallbackTotal prometheus.Counter
	// AI provider spend per user is intentionally NOT exposed here.
	// User-id labels would blow up cardinality at 1000 vehicles. The
	// existing internal/ai/usage.go writes per-user totals to
	// Postgres; surface those via a Grafana SQL data source if/when
	// we want to chart them.
	AIRequestsTotal *prometheus.CounterVec

	// Transactional email outcomes. `provider` is the wired email
	// backend (today: "resend"), `status` is "ok" / "error" /
	// "rate_limited" / "not_configured". An alert rule watches the
	// monthly increase against Resend's free-tier cap (3000) so we
	// see the wall coming a few days out, not the morning after.
	EmailSendTotal *prometheus.CounterVec

	// Signup-to-first-vehicle funnel. One increment per step a user
	// reaches (signup_created, rivian_login_attempt, rivian_connected,
	// rivian_no_vehicles, ...; see internal/api/funnel.go for the
	// full set). `event` is a fixed vocabulary, never a user id, so
	// cardinality stays bounded. The per-user trail lives in the
	// matching "funnel" log line instead.
	FunnelEventsTotal *prometheus.CounterVec

	// Authenticated requests by how they were authenticated: cookie, kratos,
	// header (trusted-proxy SSO), bypass. A fixed vocabulary; no user data.
	// (Impersonation is logged but not counted separately: it rides on a
	// cookie-authenticated admin.)
	AuthRequestsTotal *prometheus.CounterVec

	// Server-side funnel events by outcome: recorded, skipped_dnt (the request
	// sent Do Not Track / GPC), skipped_disabled, skipped_other, failed. Event
	// name and outcome only; no user data.
	ServerEventsTotal *prometheus.CounterVec

	// Completed vehicle sleeps by outcome: "failed" when the car woke
	// within a minute of falling asleep, "lasted" otherwise. A rising
	// failed share is a car that can't stay asleep (vampire drain);
	// the per-vehicle detail is in the "vehicle sleep ended" log line.
	VehicleSleepsTotal *prometheus.CounterVec

	// Parallax drive-dynamics values ignored instead of applied, by topic:
	// cloud replays of a topic's last snapshot (every resubscribe re-sends
	// it with its original timestamp) and frames older than a minute. A
	// replayed gear D once flapped a charging truck's sessions.
	ParallaxReplaysIgnoredTotal *prometheus.CounterVec
	// Charges whose energy the recorder replaced with an estimate from the SoC gain because its integral had lost energy
	// (a handoff or a restart). No labels: /metrics is public. Every increment is a recorder bug to find.
	ChargeEnergyFloorTotal prometheus.Counter

	// APNs device registrations refused because the token is registered to another account whose session is live and the
	// caller was not the same install, by reason (install_mismatch or install_missing). No token, user or hash: /metrics is public.
	APNsRegisterRefusedTotal *prometheus.CounterVec

	// Passkey registrations and sign-ins refused, by a fixed reason (ceremony, unknown_credential, assertion,
	// no_user_verification, sign_count, registration). No credential, user or address: /metrics is public.
	PasskeyRefusedTotal *prometheus.CounterVec
	// LogoutForgetAttemptsTotal and LogoutForgetFailuresTotal count the identity provider's "forget" step (Hydra's remembered login,
	// Kratos' sessions) wherever Rivolt revokes sessions: by entry point and, for failures, which step failed. No identity labels.
	LogoutForgetAttemptsTotal *prometheus.CounterVec
	LogoutForgetFailuresTotal *prometheus.CounterVec

	// vehicleState frames carrying an otaStatus, by its class (a fixed list of expected words, else "other"): what
	// states a software update goes through, which only "Idle" has been seen to say so far.
	OTAStatusFramesTotal *prometheus.CounterVec

	// Per-wheel tire status on vehicleState frames that carry tire data, by class: low (one of Rivian's low words),
	// not_low (normal or ok), other (any other word) or missing (a pressure with no status). A fixed list; no vehicle,
	// user or raw word. It tells whether prod trucks ever say low before any alert relies on it.
	TireStatusTotal *prometheus.CounterVec
	// RequestAcquiresWithoutUser counts request-pool connections handed out for a context with no user, by route pattern.
	RequestAcquiresWithoutUser *prometheus.CounterVec

	// Demo rate-limit bucket keys by source: "cf" / "lb" when the direct
	// peer is a Cloudflare edge / our private load-balancer hop and
	// CF-Connecting-IP was used; "xff" when the key is the rightmost
	// X-Forwarded-For entry itself (through the Cloudflare tunnel that is
	// the visitor, appended by Cloudflare: a per-visitor key); "peer" when
	// there was no forwarded address and the connection address is the key
	// (a shared bucket behind an unforwarded proxy). Only a high "peer"
	// share on a proxied install signals visitors sharing buckets.
	DemoClientKeyTotal *prometheus.CounterVec

	// Demo requests rejected by a rate limiter (HTTP 429), by route class
	// (general, samples, heavy, tiles) and by which budget bound (client
	// or global). A sustained global count is the signal to look at; no
	// addresses are recorded.
	DemoRateLimitedTotal *prometheus.CounterVec

	// Failed live recorder writes by kind (drive_upsert, charge_upsert,
	// stale_charge). Nonzero means drives or charges are being lost or left
	// stale, typically a database outage.
	RecorderWriteFailuresTotal *prometheus.CounterVec
	// Sign-in lockouts: an email address reached the failed-login limit and is
	// refused for the rest of the window. No labels, so no address is recorded.
	// A burst is a guessing or targeted-lockout campaign.
	LoginLockoutsTotal prometheus.Counter
	// FailLimiterFallbackTotal counts failed-login limiter calls Redis could not serve, which
	// fell back to the per-pod count (the cross-replica count was unavailable). Alert on a
	// sustained rate: the limit is then up to pods x max.
	FailLimiterFallbackTotal prometheus.Counter
	// WebSocket "next" frames dropped because a subscription's 16-slot channel
	// was full (a consumer stuck behind a slow database). A missed gear frame
	// can merge or lose a drive.
	WSFramesDroppedTotal prometheus.Counter

	// Non-transactional messages (internal/engage), by kind and outcome (sent,
	// failed, already_sent, skipped_*). No user ids in labels.
	EngagementSendsTotal *prometheus.CounterVec
	// Claims left in 'sending' past 15 minutes: a crash between claim and
	// delivery. Never resent, so a nonzero value is how a lost message shows.
	EngagementStuckSending prometheus.Gauge
	// vehicle_state partitions dropped by sample retention.
	SamplePartitionsDropped prometheus.Counter
	// Archive objects that exist but fail to read back (corrupt, truncated):
	// for a thinned month, full-resolution data lost. Alert on any.
	ArchiveReadCorruptTotal prometheus.Counter
	// Archive objects flagged for re-archiving because their month's rows no
	// longer matched them (a write during a month's first archive).
	ArchiveRepairsTotal prometheus.Counter
	// Telemetry archive objects (one per user, vehicle and month) by outcome
	// (verified, failed), and the bytes of the verified ones.
	ArchiveObjectsTotal *prometheus.CounterVec
	ArchiveBytesTotal   prometheus.Counter
	// Archive deletions (account deletion, history reset): objects deleted,
	// and requests pending longer than archive.DeleteWithin (alert on > 0).
	ArchiveObjectsDeletedTotal prometheus.Counter
	ArchiveDeletionsOverdue    prometheus.Gauge
	// Unix time of the deletion job's last completed run, set to the start
	// time when the job starts: an alert on its age catches a pod whose runs
	// never complete (the overdue gauge is only set by a completed run).
	// 0 while the archive is off.
	ArchiveDeletionsLastRun prometheus.Gauge
}

// New constructs a Metrics with all collectors registered against a
// fresh registry. Pass the returned *Metrics into any package that
// needs to record a sample. Call Handler() to mount /metrics.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	m := &Metrics{
		reg: reg,

		HTTPRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_http_requests_total",
				Help: "Total HTTP requests handled, partitioned by route and status code.",
			},
			// `route` is the chi route pattern (e.g. /api/state/{vehicleID}),
			// NOT the raw URL — keeps cardinality bounded as vehicles
			// scale up. Status is bucketed by family (2xx/3xx/4xx/5xx)
			// for the same reason.
			[]string{"method", "route", "status"},
		),
		HTTPRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "rivolt_http_request_duration_seconds",
				Help:    "End-to-end HTTP handler latency, partitioned by route.",
				Buckets: prometheus.DefBuckets, // 5ms..10s, fine for web traffic
			},
			[]string{"method", "route"},
		),
		RivianResultsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_rivian_results_total",
				Help: "Outcome class for each call against Rivian's GraphQL gateway.",
			},
			// `op` is the GraphQL operation name (e.g. login,
			// vehicleState, currentUser), `class` is from
			// internal/rivian/errclass: success / transient / outage /
			// rate_limited / user_action / unknown.
			[]string{"op", "class"},
		),
		SubscriptionLeases: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "rivolt_subscription_leases",
				Help: "Rivian websocket subscription leases currently owned by this pod.",
			},
		),
		LeaseGroupsOwned: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "rivolt_lease_groups_owned",
				Help: "User-group leases (account_leases) currently held by this pod.",
			},
		),
		LeaseFenceWouldReject: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_lease_fence_would_reject_total",
				Help: "Group lease tokens held by this pod that the fence check would have refused (dry run: nothing is refused yet). source is db (rivolt_lease_ok) or local (renewals failing past the local deadline).",
			},
			[]string{"kind", "source"},
		),
		LeaseUnstreamed: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "rivolt_lease_unstreamed_total",
				Help: "Episodes of a Rivian subscription lease held by this pod with no subscription running (re-subscribed by the lease coordinator).",
			},
		),
		RivianAccountMismatch: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "rivolt_rivian_account_mismatch",
			Help: "Loaded Rivian accounts that still disagree with the database after a sync round, by field.",
		}, []string{"field"}),
		RivianAccountSyncErrorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_rivian_account_sync_errors_total",
			Help: "Sync rounds that could not read the Rivian account state from the database.",
		}),
		RivianSessionsExpiring: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "rivolt_rivian_sessions_expiring",
			Help: "Stored Rivian sessions with at most this much time left before the expected 180-day end.",
		}, []string{"within"}),
		RivianSessionsExpired: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rivolt_rivian_sessions_expired_unflagged",
			Help: "Stored Rivian sessions past their expected end that are not flagged needs_reauth.",
		}),
		RivianExpiryNoticesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_rivian_expiry_notices_total",
			Help: "Rivian session expiry warnings, by days left and outcome.",
		}, []string{"days", "outcome"}),
		ReauthRaiseDeferred: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "rivolt_rivian_reauth_raise_deferred_total",
				Help: "Rejected Rivian calls whose needs_reauth raise was held back because the client did not yet know its stored session version (the next account sync decides).",
			},
		),
		RivianBreakerState: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "rivolt_rivian_breaker_state",
				Help: "Rivian upstream breaker state. 0=closed, 1=half_open, 2=open.",
			},
		),
		RivianBreakerTrips: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_rivian_breaker_trips_total",
				Help: "Times the breaker tripped to open, partitioned by reason.",
			},
			[]string{"reason"},
		),
		RivianBudgetFallbackTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_rivian_budget_fallback_total",
				Help: "Outbound Rivian calls gated by the per-process bucket because Redis could not serve the global one, by class.",
			},
			[]string{"class"},
		),
		PlanQuotaFallbackTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_plan_quota_fallback_total",
			Help: "Trip-planner quota checks counted in this process because Redis could not serve them.",
		}),
		RivianRateLimitBlocked: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_rivian_ratelimit_blocked_total",
				Help: "Outbound calls denied by the global token bucket, partitioned by class.",
			},
			[]string{"class"},
		),
		AIRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_ai_requests_total",
				Help: "AI provider calls, by provider and outcome.",
			},
			[]string{"provider", "outcome"},
		),
		EmailSendTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_email_send_total",
				Help: "Transactional emails attempted, partitioned by provider and outcome.",
			},
			[]string{"provider", "status"},
		),
		DemoClientKeyTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_demo_client_key_total",
				Help: "Demo rate-limit client keys, by source (cf/lb = CF-Connecting-IP trusted from a Cloudflare/private peer, xff = the rightmost X-Forwarded-For visitor address, peer = the connection address).",
			},
			[]string{"source"},
		),
		DemoRateLimitedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_demo_rate_limited_total",
				Help: "Demo requests answered 429, by route class and which budget (client or global) bound.",
			},
			[]string{"route_class", "scope"},
		),
		RecorderWriteFailuresTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_recorder_write_failures_total",
				Help: "Failed live recorder writes, by kind.",
			},
			[]string{"kind"},
		),
		LoginLockoutsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_login_lockouts_total",
			Help: "Times an email address reached the failed-login limit and was locked out.",
		}),
		FailLimiterFallbackTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_fail_limiter_fallback_total",
			Help: "Failed-login limiter calls Redis could not serve; the per-pod count was used instead.",
		}),
		WSFramesDroppedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_ws_frames_dropped_total",
			Help: "Rivian WebSocket frames dropped because a subscription consumer was behind.",
		}),
		EngagementSendsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_engagement_sends_total",
				Help: "Non-transactional messages by kind and outcome.",
			},
			[]string{"kind", "outcome"},
		),
		EngagementStuckSending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rivolt_engagement_stuck_sending",
			Help: "Engagement claims left in sending for over 15 minutes.",
		}),
		SamplePartitionsDropped: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_sample_partitions_dropped_total",
			Help: "Monthly vehicle_state partitions dropped by sample retention (RIVOLT_SAMPLE_RETENTION_MONTHS).",
		}),
		ArchiveRepairsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_archive_repairs_total",
			Help: "Archive objects flagged for re-archiving because their month's rows no longer matched them.",
		}),
		ArchiveReadCorruptTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_archive_read_corrupt_total",
			Help: "Telemetry archive objects that exist but failed to read back (corrupt, truncated); for a thinned month, full-resolution data lost.",
		}),
		ArchiveObjectsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_archive_objects_total",
			Help: "Telemetry archive objects (one per user, vehicle and month) written, by outcome: verified (read back and matched) or failed.",
		}, []string{"outcome"}),
		ArchiveBytesTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_archive_bytes_total",
			Help: "Bytes of verified telemetry archive objects written.",
		}),
		ArchiveObjectsDeletedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_archive_objects_deleted_total",
			Help: "Telemetry archive objects deleted for account deletions and history resets.",
		}),
		ArchiveDeletionsOverdue: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rivolt_archive_deletions_overdue",
			Help: "Archive deletion requests still pending after 24 hours (the deletion promise).",
		}),
		ArchiveDeletionsLastRun: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rivolt_archive_deletions_last_run_timestamp_seconds",
			Help: "Unix time of the archive deletion job's last completed run (the job's start time until its first run); 0 while the archive is off.",
		}),
		FunnelEventsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_funnel_events_total",
				Help: "Signup-to-first-vehicle funnel steps reached, partitioned by event.",
			},
			[]string{"event"},
		),
		ServerEventsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_server_events_total",
				Help: "Server-side funnel events (signup_complete, rivian_connect_ok) by outcome.",
			},
			[]string{"event", "outcome"},
		),
		AuthRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_auth_requests_total",
				Help: "Authenticated requests, by how they were authenticated (cookie, kratos, header, bypass).",
			},
			[]string{"method"},
		),
		ParallaxReplaysIgnoredTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_parallax_replays_ignored_total",
				Help: "Parallax drive-dynamics values ignored as replays or stale, by topic.",
			},
			[]string{"topic"},
		),
		ChargeEnergyFloorTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_charge_energy_floor_total",
			Help: "Charges whose energy was replaced by an estimate from the SoC gain (the recorder's integral had lost energy).",
		}),
		LogoutForgetAttemptsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_logout_forget_attempts_total",
				Help: "Times the identity provider's remembered login and sessions were cleared after a revoke, by entry point (logout|sign_out_one|sign_out_others|password|secure|admin_secure).",
			},
			[]string{"entry"},
		),
		LogoutForgetFailuresTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_logout_forget_failures_total",
				Help: "Failures of that clearing, by entry point and step (account_lookup|identity_lookup|hydra_login|hydra_consent|kratos_sessions|other). A failure leaves a remembered browser able to sign back in.",
			},
			[]string{"entry", "step"},
		),
		PasskeyRefusedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_passkey_refused_total",
				Help: "Passkey registrations and sign-ins refused, by reason.",
			},
			[]string{"reason"},
		),
		APNsRegisterRefusedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_apns_register_refused_total",
				Help: "APNs device registrations refused as belonging to another account's live session, by reason.",
			},
			[]string{"reason"},
		),
		OTAStatusFramesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_ota_status_frames_total",
				Help: "vehicleState frames carrying an otaStatus, by class (a fixed list of expected words, else other).",
			},
			[]string{"status"},
		),
		TireStatusTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_tire_status_total",
				Help: "Per-wheel tire status on vehicleState frames, by class (low|not_low|other|missing).",
			},
			[]string{"status"},
		),
		RequestAcquiresWithoutUser: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_request_acquires_without_user_total",
				Help: "Request-pool connections handed out for a request context with no user (those queries see no rows), by chi route pattern (never the path; \"none\" off a request, \"unmatched\" for a 404).",
			},
			[]string{"route"},
		),
		VehicleSleepsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_vehicle_sleeps_total",
				Help: "Completed vehicle sleeps, partitioned by outcome (lasted|failed).",
			},
			[]string{"outcome"},
		),
	}

	reg.MustRegister(
		m.HTTPRequestsTotal,
		m.HTTPRequestDuration,
		m.RivianResultsTotal,
		m.SubscriptionLeases,
		m.LeaseGroupsOwned,
		m.LeaseFenceWouldReject,
		m.LeaseUnstreamed,
		m.ReauthRaiseDeferred,
		m.RivianAccountMismatch,
		m.RivianAccountSyncErrorsTotal,
		m.RivianSessionsExpiring,
		m.RivianSessionsExpired,
		m.RivianExpiryNoticesTotal,
		m.RivianBreakerState,
		m.RivianBreakerTrips,
		m.RivianRateLimitBlocked,
		m.RivianBudgetFallbackTotal,
		m.PlanQuotaFallbackTotal,
		m.AIRequestsTotal,
		m.EmailSendTotal,
		m.DemoClientKeyTotal,
		m.DemoRateLimitedTotal,
		m.LoginLockoutsTotal,
		m.FailLimiterFallbackTotal,
		m.EngagementSendsTotal,
		m.RecorderWriteFailuresTotal,
		m.WSFramesDroppedTotal,
		m.EngagementStuckSending,
		m.SamplePartitionsDropped,
		m.ArchiveReadCorruptTotal,
		m.ArchiveRepairsTotal,
		m.ArchiveObjectsTotal,
		m.ArchiveBytesTotal,
		m.ArchiveObjectsDeletedTotal,
		m.ArchiveDeletionsOverdue,
		m.ArchiveDeletionsLastRun,
		m.FunnelEventsTotal,
		m.AuthRequestsTotal,
		m.ServerEventsTotal,
		m.VehicleSleepsTotal,
		m.ParallaxReplaysIgnoredTotal,
		m.ChargeEnergyFloorTotal,
		m.APNsRegisterRefusedTotal,
		m.PasskeyRefusedTotal,
		m.LogoutForgetAttemptsTotal,
		m.LogoutForgetFailuresTotal,
		m.OTAStatusFramesTotal,
		m.TireStatusTotal,
		m.RequestAcquiresWithoutUser,
	)
	return m
}

// Handler returns the http.Handler for /metrics scoped to this
// Metrics' private registry. Mount under /metrics with no auth — the
// Prometheus scraper inside the cluster reaches it via the pod IP,
// and the Ingress doesn't expose it externally (see
// deploy/helm/rivolt/templates/ingress.yaml).
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		// Surface scrape errors in the response so kube-prometheus-stack
		// flags the target unhealthy instead of silently dropping
		// metrics.
		ErrorHandling: promhttp.HTTPErrorOnError,
	})
}

// Registry returns the underlying registry. Tests use this to
// gather expected samples without going through the HTTP handler.
func (m *Metrics) Registry() *prometheus.Registry {
	return m.reg
}

// RegisterDBPool exposes a database/sql pool's saturation under name ("system"): the
// connections in use and the limit, and how many callers had to wait for one and for how
// long. Waits are the signal that the pool is too small for the load; a sustained rate
// after the pool was cut to 10 per pod means raise RIVOLT_DB_MAX_CONNS, not that the
// database is slow. No labels beyond the pool's name.
func (m *Metrics) RegisterDBPool(name string, d *sql.DB) {
	if m == nil || d == nil {
		return
	}
	lbl := prometheus.Labels{"pool": name}
	m.reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "rivolt_db_pool_in_use", Help: "Connections in use in the pool.", ConstLabels: lbl},
			func() float64 { return float64(d.Stats().InUse) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "rivolt_db_pool_max_open", Help: "The pool's connection limit.", ConstLabels: lbl},
			func() float64 { return float64(d.Stats().MaxOpenConnections) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "rivolt_db_pool_wait_total", Help: "Callers that had to wait for a free connection.", ConstLabels: lbl},
			func() float64 { return float64(d.Stats().WaitCount) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "rivolt_db_pool_wait_seconds_total", Help: "Total time callers waited for a free connection.", ConstLabels: lbl},
			func() float64 { return d.Stats().WaitDuration.Seconds() }),
	)
}
