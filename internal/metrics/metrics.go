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

	"github.com/apohor/rivolt/internal/applebill"
	"github.com/apohor/rivolt/internal/launchnotice"
	"github.com/apohor/rivolt/internal/stripebill"
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
	// LeaseFenced counts owner-side writes refused or skipped because this pod no longer holds the
	// group lease, by write class (samples, ...). Alert on > 0 for 10 minutes.
	LeaseFenced *prometheus.CounterVec
	// RivianScheduleReadTotal counts charging-schedule reads by outcome: ok, retried_ok, dropped (refused
	// by the shared budget on every try) and error. Pre-created at 0; no other labels.
	RivianScheduleReadTotal *prometheus.CounterVec
	// LeaseShed counts the lead-mode shed decisions of a pod over its fair share, by choice: idle
	// (a sleeping group), awake (no sleeping one), deferred (every group over the share had an open
	// drive or charge, so nothing moved that pass).
	LeaseShed *prometheus.CounterVec
	// LeaseUnstreamed counts episodes of a lease this pod holds without
	// a subscription behind it (the coordinator re-subscribes; alert on
	// any increase).
	LeaseUnstreamed prometheus.Counter
	// LeaseSelfFence counts times this pod stopped every subscription because its group lease
	// renewals failed past the local deadline (a slow or unreachable database).
	LeaseSelfFence prometheus.Counter
	// EntitlementsLookupErrors counts failed reads of an account's Pro entitlement rows. The plan fails open (the account counts as Pro
	// where an answer is needed), so a sustained failure is invisible to users and shows only here.
	EntitlementsLookupErrors prometheus.Counter
	// StripeCancelPending and StripeCancelOldestSeconds are the queue of Stripe cancellations of deleted accounts that have not worked yet:
	// how many, and how long the oldest has waited. Anything above 0 for more than an hour means a deleted person may still be billed:
	// the alert.
	StripeCancelPending       prometheus.Gauge
	StripeCancelOldestSeconds prometheus.Gauge
	// Stripe's webhook endpoint, counts only: StripeWebhookEvents by event type (a fixed set, "other" for any type Rivolt does not handle) and
	// outcome (applied, duplicate, ignored, unmapped, stale, rejected); StripeWebhookLag is how long after Stripe created an event Rivolt
	// received it (a delivery that Stripe had to retry shows as a long lag). BillingEvents counts billing moments an operator may need to see
	// (kind payment_failed: a subscription went past due), pre-created at 0.
	StripeWebhookEvents *prometheus.CounterVec
	StripeWebhookLag    prometheus.Histogram
	BillingEvents       *prometheus.CounterVec
	// Entitlements is the count of entitlement rows by source and status and Accounts the number of accounts, both without the demo account and
	// without any user label, refreshed every few minutes; BillingEnabled is 1 when RIVOLT_BILLING is on.
	Entitlements   *prometheus.GaugeVec
	Accounts       prometheus.Gauge
	BillingEnabled prometheus.Gauge
	// LeaseRenew counts lease renew attempts by scope (vehicle: the per-vehicle renew; group: the lead-mode group pass) and
	// outcome: ok, lost (the renew worked and something we held was gone), timeout, error. A pod whose renews stop working shows
	// here before it self-fences.
	LeaseRenew *prometheus.CounterVec
	// LeaseAcquireToFrame is the seconds from a vehicle lease being acquired to the first frame
	// recorded after it, by state (active: open drive or charge; idle). The flag-on bar is active p95 <= 12 s.
	LeaseAcquireToFrame *prometheus.HistogramVec
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
	// bypass. A fixed vocabulary; no user data.
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
	// Closed drives that moved while the truck reported no live location (at most one distinct fix): "how many a day" without a log query.
	DrivesLocationUnavailableTotal prometheus.Counter

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
	// RateLimiterFallbackTotal counts requests a rate limiter (per IP or per user) decided from its per-pod
	// bucket because Redis could not answer, by limiter name (a fixed set of route names, never an address or a user). Alert on a sustained
	// rate: the limits are then up to pods x limit.
	RateLimiterFallbackTotal *prometheus.CounterVec
	// WebSocket "next" frames dropped because a subscription's 16-slot channel
	// was full (a consumer stuck behind a slow database). A missed gear frame
	// can merge or lose a drive.
	WSFramesDroppedTotal prometheus.Counter

	// Non-transactional messages (internal/engage), by kind and outcome (sent,
	// failed, already_sent, skipped_*). No user ids in labels.
	EngagementSendsTotal *prometheus.CounterVec
	// Periodic jobs that run on one replica at a time (internal/singleton), by job and outcome (ran, skipped, error). "skipped" is another
	// replica holding the job's lock: normal at N replicas, one per tick per extra pod. "error" is the lock itself failing.
	SingletonRunsTotal *prometheus.CounterVec
	// The dated Pro notices (internal/launchnotice), by kind and outcome (sent, in_app, skipped, failed, late). "late" counts a founding
	// year that had to be extended to keep the 30 days' notice the Terms promise: any nonzero value is worth a look.
	DatedNoticesTotal *prometheus.CounterVec
	// App Store Server Notifications by outcome (applied, duplicate, ignored, unmapped, stale, rejected); a rejected one is a bad signature or
	// a bundle, environment or product that is not this deployment's. Pre-created at 0.
	AppleNotificationsTotal *prometheus.CounterVec
	// BillingNonUSTotal counts Stripe customers whose billing address is outside the US (Rivolt Pro is sold in the US only), once per customer
	// and country per process. The account keeps Pro; the operator refunds and cancels, so any increase is worth an alert. Starts at 0.
	BillingNonUSTotal prometheus.Counter
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
		LeaseAcquireToFrame: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "rivolt_lease_acquire_to_frame_seconds",
				Help:    "Seconds from this pod acquiring a vehicle's lease to the first frame received for it, by state: active (an open drive or charge when the frame was seen) or idle (parked: the first push depends on Rivian's cadence).",
				Buckets: []float64{1, 2, 3, 5, 8, 12, 20, 30, 60, 120, 300},
			},
			[]string{"state"},
		),
		LeaseSelfFence: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "rivolt_lease_self_fence_total",
				Help: "Times this pod stopped every subscription because its group lease renewals failed past the local deadline.",
			},
		),
		EntitlementsLookupErrors: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "rivolt_entitlements_lookup_errors_total",
				Help: "Failed reads of an account's Pro entitlement rows (the plan fails open).",
			},
		),
		StripeCancelPending: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "rivolt_stripe_cancel_pending",
				Help: "Stripe cancellations of deleted accounts still queued (the cancel failed or Stripe is not configured here).",
			},
		),
		StripeCancelOldestSeconds: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "rivolt_stripe_cancel_oldest_seconds",
				Help: "Seconds the oldest queued Stripe cancellation of a deleted account has waited (0 when none).",
			},
		),
		StripeWebhookEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_stripe_webhook_events_total",
			Help: "Stripe webhook deliveries by event type and outcome (applied, duplicate, ignored, unmapped, stale, rejected).",
		}, []string{"type", "outcome"}),
		StripeWebhookLag: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "rivolt_stripe_webhook_lag_seconds",
			Help:    "Seconds between Stripe creating an event and Rivolt receiving it (retried deliveries show as a long lag).",
			Buckets: []float64{1, 5, 15, 60, 300, 900, 3600, 21600, 86400},
		}),
		BillingEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_billing_events_total",
			Help: "Billing moments an operator may need to see, by kind (payment_failed: a subscription went past due).",
		}, []string{"kind"}),
		Entitlements: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "rivolt_entitlements",
			Help: "Entitlement rows by source and status, without the demo account; no user labels.",
		}, []string{"source", "status"}),
		Accounts: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rivolt_accounts",
			Help: "Accounts, without the demo account.",
		}),
		BillingEnabled: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rivolt_billing_enabled",
			Help: "1 when RIVOLT_BILLING is on, else 0.",
		}),
		LeaseRenew: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_lease_renew_total",
				Help: "Lease renew attempts by scope (vehicle or group) and outcome (ok, lost, timeout, error).",
			},
			[]string{"scope", "outcome"},
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
				Help: "Group lease tokens held by this pod that the fence check would have refused (dry run). source is db (rivolt_lease_ok) or local (renewals failing past the local deadline).",
			},
			[]string{"source"},
		),
		LeaseShed: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_lease_shed_total",
				Help: "Lead-mode shed decisions of a pod over its fair share, by choice (idle, awake, deferred: every group over the share had an open drive or charge).",
			},
			[]string{"choice"},
		),
		RivianScheduleReadTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_rivian_schedule_read_total",
				Help: "Charging-schedule reads by outcome: ok, retried_ok (worked after a refusal by the shared budget), dropped (refused on every try) or error.",
			},
			[]string{"outcome"},
		),
		LeaseFenced: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_lease_fenced_total",
				Help: "Owner-side writes refused or skipped because this pod no longer holds the group lease, by write class.",
			},
			[]string{"kind"},
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
		RateLimiterFallbackTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_rate_limiter_fallback_total",
			Help: "Requests a rate limiter decided from its per-pod bucket because Redis could not answer, by limiter.",
		}, []string{"limiter"}),
		WSFramesDroppedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_ws_frames_dropped_total",
			Help: "Rivian WebSocket frames dropped because a subscription consumer was behind.",
		}),
		SingletonRunsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_singleton_runs_total",
				Help: "Singleton periodic job attempts by job and outcome (ran, skipped, error).",
			},
			[]string{"job", "outcome"},
		),
		EngagementSendsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_engagement_sends_total",
				Help: "Non-transactional messages by kind and outcome.",
			},
			[]string{"kind", "outcome"},
		),
		BillingNonUSTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_billing_non_us_total",
			Help: "Stripe customers with a billing address outside the US, once per customer and country per process.",
		}),
		AppleNotificationsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_apple_notifications_total",
				Help: "App Store Server Notifications by outcome.",
			},
			[]string{"outcome"},
		),
		DatedNoticesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_dated_notices_total",
				Help: "Dated Pro notices (founding year end, annual renewal) by kind and outcome.",
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
		DrivesLocationUnavailableTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rivolt_drives_location_unavailable_total",
			Help: "Closed drives that moved while the vehicle reported no live location.",
		}),
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
		m.LeaseSelfFence,
		m.LeaseRenew,
		m.EntitlementsLookupErrors,
		m.StripeCancelPending,
		m.StripeCancelOldestSeconds,
		m.StripeWebhookEvents,
		m.StripeWebhookLag,
		m.BillingEvents,
		m.Entitlements,
		m.Accounts,
		m.BillingEnabled,
		m.LeaseAcquireToFrame,
		m.LeaseFenceWouldReject,
		m.LeaseFenced,
		m.RivianScheduleReadTotal,
		m.LeaseShed,
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
		m.RateLimiterFallbackTotal,
		m.EngagementSendsTotal,
		m.SingletonRunsTotal,
		m.DatedNoticesTotal,
		m.AppleNotificationsTotal,
		m.BillingNonUSTotal,
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
		m.DrivesLocationUnavailableTotal,
		m.APNsRegisterRefusedTotal,
		m.PasskeyRefusedTotal,
		m.LogoutForgetAttemptsTotal,
		m.LogoutForgetFailuresTotal,
		m.OTAStatusFramesTotal,
		m.TireStatusTotal,
		m.RequestAcquiresWithoutUser,
	)
	// The lease counters exist from boot at 0, so the first fenced write or the first unmatched source
	// is an increase (a series that first appears at 1 has increase() == 0, and the alert would miss the
	// very event it is for). Keep the label values in step with the callers.
	for _, outcome := range applebill.Outcomes {
		m.AppleNotificationsTotal.WithLabelValues(outcome).Add(0)
	}
	// The dated Pro notices: every (kind, outcome) the jobs can report, so the first "late" is an increase.
	for _, kind := range launchnotice.MetricKinds {
		for _, outcome := range launchnotice.Outcomes {
			m.DatedNoticesTotal.WithLabelValues(kind, outcome).Add(0)
		}
	}
	// The Stripe webhook: every (type, outcome) and the billing moments, so the first rejected or unmapped delivery is an increase.
	for _, typ := range stripebill.MetricTypes {
		for _, outcome := range stripebill.MetricOutcomes {
			m.StripeWebhookEvents.WithLabelValues(typ, outcome).Add(0)
		}
	}
	m.BillingEvents.WithLabelValues("payment_failed").Add(0)
	for _, kind := range LeaseFencedKinds {
		m.LeaseFenced.WithLabelValues(kind).Add(0)
	}
	for _, class := range []string{"priority", "main", "background"} {
		m.RivianRateLimitBlocked.WithLabelValues(class).Add(0) // exported at 0 so the first refusal is an increase
	}
	for _, outcome := range []string{"ok", "retried_ok", "dropped", "error"} {
		m.RivianScheduleReadTotal.WithLabelValues(outcome).Add(0)
	}
	m.EntitlementsLookupErrors.Add(0) // exported at 0 so the first failure is an increase
	for _, scope := range []string{"vehicle", "group"} {
		for _, outcome := range []string{"ok", "lost", "timeout", "error"} {
			m.LeaseRenew.WithLabelValues(scope, outcome).Add(0)
		}
	}
	for _, state := range []string{"active", "idle"} {
		m.LeaseAcquireToFrame.WithLabelValues(state) // exported at count 0 from boot
	}
	for _, source := range []string{"db", "local"} {
		m.LeaseFenceWouldReject.WithLabelValues(source).Add(0)
	}
	for _, choice := range LeaseShedChoices {
		m.LeaseShed.WithLabelValues(choice).Add(0)
	}
	return m
}

// LeaseShedChoices are the shed decisions rivolt_lease_shed_total counts (leases.Coordinator.chooseShed).
var LeaseShedChoices = []string{"idle", "awake", "deferred"}

// LeaseFencedKinds are the write classes rivolt_lease_fenced_total counts.
var LeaseFencedKinds = []string{"samples", "drives", "charges", "sends", "redis", "rivian", "vehicle_info"}

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
