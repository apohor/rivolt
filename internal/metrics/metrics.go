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

// RoutePointRefusals are the rivolt_route_point_refused_total "where" values, all created at 0 at boot: fix_offglobe (a position off the
// globe), fill_globe (a route-fill with such a vertex, caught by the recorder), fill_offglobe and fill_detour (the routing engine's shape
// failed its own check: off the globe, or nowhere near the two ends), encode_skip (the encoder's last line), plan_offglobe and plan_detour (a served plan's line failed the same checks and was left out).
var RoutePointRefusals = []string{"fix_offglobe", "fill_globe", "fill_offglobe", "fill_detour", "encode_skip", "plan_offglobe", "plan_detour"}

// TripPlanStages are the rivolt_trip_plan_stage_seconds stages: our own work around the planner call, so Rivian's time (the upstream
// histogram) can be told from ours. geometry is the road line through the stops (up to three Valhalla routes, one after another);
// enrich_load reads the user's vehicle profile, charging settings, networks and own rates; enrich is the weather adjustment and the costs; climb is the DEM read along each leg.
var TripPlanStages = []string{"geometry", "enrich_load", "enrich", "climb", "radar"}

// BillingRouteStatuses are the (route, statuses) of rivolt_http_requests_total created at 0 at boot, POST only: the billing routes whose first error
// must not go unseen. A counter series is born at its first request, at 1, and increase() or rate() over a series that starts at 1 sees no change:
// the first 4xx or 5xx on a freshly started pod was invisible to the Stripe webhook panel and to RivoltStripeWebhook5xx / RivoltBillingStartFailing
// (found in flip rehearsal 2). With the series at 0 from boot the first error is an increase. A status not listed here is still counted, but its
// first request per pod is not visible to increase(); keep the list in step with the statuses those alerts and panels watch (4.. and 5..).
var BillingRouteStatuses = map[string][]string{
	"/api/billing/stripe/webhook":      {"400", "401", "403", "500", "502", "503", "504"},
	"/api/billing/apple/notifications": {"400", "500", "503"},
	"/api/billing/checkout":            {"400", "402", "403", "409", "500", "502", "503"},
	"/api/billing/portal":              {"400", "403", "409", "500", "502", "503"},
	"/api/billing/apple/transaction":   {"400", "403", "409", "500", "503"},
}

// TripPlanLegResults are the rivolt_trip_plan_legs_total results, all created at 0 at boot.
var TripPlanLegResults = []string{"hit", "miss", "quota", "error"}

// AppleLinkOutcomes are the rivolt_apple_link_total outcomes, all created at 0 at boot: started (a link began), linked,
// in_use (that Apple ID belongs to another account), account_linked (this account has another Apple ID), expired (no
// open request, a spent one, or the session ended), reauth_refused (start without the right password), password_needed (the account has no password to link with: Google or GitHub only), unlinked, unlink_refused (the password check refused an unlink),
// provider_error (Apple or our own failure on the way).
var AppleLinkOutcomes = []string{"started", "linked", "in_use", "account_linked", "expired", "reauth_refused", "password_needed", "unlinked", "unlink_refused", "provider_error"}

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
	// RivianCallRefused counts Rivian calls the monitor did not make because this pod does not hold the vehicle's group, by site
	// (single-owner M4-1). Every site is created at 0. A non-zero value at seed, refresh_once or ws_dial is a finding: nothing should
	// have tried.
	RivianCallRefused *prometheus.CounterVec
	// RivianRequestSeconds is how long each request that reached Rivian's GraphQL gateway took, by operation (a fixed set, rivian.PermitOps
	// without ws_dial). Every operation is created at 0. Nothing per user or vehicle.
	RivianRequestSeconds *prometheus.HistogramVec
	// RivianWSReconnects counts vehicleState subscriptions opened again, by why (rivian.WSReconnectReasons). Created at 0.
	RivianWSReconnects *prometheus.CounterVec
	// RivianUnpermitted counts requests to Rivian that carried neither a fence token nor a holder marker (RIVOLT_RIVIAN_PERMIT=observe or
	// enforce); in enforce mode each was refused. Created at 0. Non-zero is a bug finding (single-owner M4-1b).
	RivianUnpermitted *prometheus.CounterVec
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
	// LeaseUnstreamedVehicles is how many vehicles this pod holds a lease for with no subscription running, as of the coordinator's
	// last check. The alert waits on it staying above 0 for minutes; the counter above marks each episode's start.
	LeaseUnstreamedVehicles prometheus.Gauge
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
	// OpenCharges is the open live charge rows now by state (charging: the vehicle's latest frame still reads plugged in and charging, by the
	// stale-charge janitor's own rule; not_charging: it does not; no_frame: the vehicle sent nothing in 2 days, a dormant account's leftover row), and OpenChargesOldestSeconds the age of the oldest of each since its last
	// write. A not_charging row much older than the janitor's hour is one it could not close (#1619, #1670). No vehicle or user labels.
	OpenCharges              *prometheus.GaugeVec
	OpenChargesOldestSeconds *prometheus.GaugeVec
	// ChargewatchLastSuccess is the Unix time of the last good open-charge measurement. The gauges above keep their last values when a read
	// fails, so a broken watcher would read as "all clear"; an alert on time() minus this catches it. No labels.
	ChargewatchLastSuccess prometheus.Gauge
	// VehiclesFallbackTotal counts the "linked account, no vehicle stored yet" Rivian lookups of /api/vehicles by outcome: stored (vehicles came back
	// and were stored), empty, error, skipped (inside the 5-minute window after an attempt, no Rivian call), unsynced (a connected account's rows the owner has not marked synced yet: the transition path, expected to fall to 0). A steady rate of error means an
	// account whose lookup keeps failing; none at all is the goal (single-owner M3-5a). Outcome label only.
	VehiclesFallbackTotal *prometheus.CounterVec
	// VehicleDiscoveryTotal counts the vehicle discovery job's turns by outcome (stored, empty, error, skipped, needs_reauth): linked users with no
	// vehicle row, looked up so their vehicles get a lease (single-owner M3-6). No user label. A steady error rate is an account Rivian keeps refusing.
	VehicleDiscoveryTotal *prometheus.CounterVec
	// WeatherRequestsTotal counts calls to the Open-Meteo forecast and archive APIs (ours or the public one) by endpoint (forecast, archive) and
	// outcome (ok, error, skipped_old: a self-hosted client declined a time older than its server keeps). No coordinates, no user.
	WeatherRequestsTotal *prometheus.CounterVec
	// HandoffGapSeconds is the hand-off gap of a vehicle that changed pods by a release, from the group being released to the first frame on the new
	// pod, by what the vehicle was doing at that frame (driving, charging, idle). It is two same-clock parts added: the wait (release to acquire,
	// both the database's clock; HandoffWaitSeconds) and the acquire to the first frame (this pod's clock). The old pod's stop before its release is
	// HandoffStopSeconds; the plan's gate adds the two. A group free for over 10 minutes and expired takeovers are not in it. The gate is its
	// driving and charging p95.
	HandoffGapSeconds *prometheus.HistogramVec
	// HandoffWaitSeconds is the first part of the gap on its own: how long a released group sat free before a peer took it. It is what a
	// released-group signal (G1a) shrinks.
	HandoffWaitSeconds prometheus.Histogram
	// HandoffStopSeconds is the old owner's part: seconds from a pod asking a vehicle's subscription to stop to it being gone, for hand-offs it
	// starts (a drain, a shed). A stop that did not finish within 5 s is not reported.
	HandoffStopSeconds prometheus.Histogram
	// RolloutHandoffsTotal counts vehicles a pod took from another holder, by reason: drain (a peer was shutting down), rebalance (released with
	// no peer draining: a fair-share shed or a retired candidate), expired (the holder's lease ran out), other (an open session's group re-keyed).
	RolloutHandoffsTotal *prometheus.CounterVec
	// SessionRehydrateTotal counts, per ownership of a vehicle (a roll, a crash, a rebalance), how an open drive or charge came back at
	// takeover: kind (drive, charge) by source (snapshot: the Redis snapshot; db: the charge's open row; fresh: nothing came back, and the
	// previous session had ended minutes before, so this is a split). A vehicle that was not mid-session counts nothing. No vehicle or user labels.
	SessionRehydrateTotal *prometheus.CounterVec
	// SessionSplitTotal counts drives and charges split in two by a takeover that lost the open session (SessionRehydrateTotal source fresh).
	SessionSplitTotal *prometheus.CounterVec
	// LeaseRenew counts lease renew attempts by scope (vehicle: the per-vehicle renew; group: the lead-mode group pass) and
	// outcome: ok, lost (the renew worked and something we held was gone), timeout, error. A pod whose renews stop working shows
	// here before it self-fences.
	LeaseRenew *prometheus.CounterVec
	// LeaseAcquireToFrame is the seconds from a vehicle lease being acquired to the first frame
	// recorded after it, by state (active: open drive or charge; idle). The flag-on bar is active p95 <= 12 s.
	LeaseAcquireToFrame *prometheus.HistogramVec
	// TakeoverWaitSeconds is how long a takeover's Rivian call waited for its slot in the pacing (G7), by class (dial, seed, features) and
	// priority (active: a driving or charging vehicle; idle). TakeoverStarvedTotal counts the calls that went ahead after the bound without a
	// slot, TakeoverLocalOnlyTotal the ones paced by the local bucket alone because Redis did not answer. A call that shared a session's slot
	// waits zero and is not counted in the wait histogram. All created at 0.
	TakeoverWaitSeconds    *prometheus.HistogramVec
	TakeoverStarvedTotal   *prometheus.CounterVec
	TakeoverLocalOnlyTotal *prometheus.CounterVec
	// SessionDecrypts counts decrypts of a stored Rivian session by call site (SessionDecryptSites; any other label is "other"); no user id.
	// TokensLoadedUnheld is how many clients in this pod's account registry hold a session token for a user whose group this pod does not
	// hold (single-owner M4-5 baseline: flag-off prod shows what the flag would remove; 0 with lead mode off, where the notion does not apply).
	SessionDecrypts    *prometheus.CounterVec
	TokensLoadedUnheld prometheus.Gauge
	// LeaseReconcileTotal counts the coordinator's reconcile passes by what started them: start (boot), tick (the timer), signal (a peer released
	// groups and said so, G1a) or other (a fenced write, a refused call or the first request asking for a pass). Created at 0. The share of
	// signal against tick is how much of the hand-off wait the signal removed.
	LeaseReconcileTotal *prometheus.CounterVec
	// LeaseLeadPassSeconds is the database time of each lead-mode group pass (renew, acquire, vehicle-group read); HeldGroupUsersSeconds is the
	// owner's read of the users in the groups it holds (every discovery pass and nudge). Neither carries a user or group label.
	LeaseLeadPassSeconds  prometheus.Histogram
	HeldGroupUsersSeconds prometheus.Histogram
	// ReauthRaiseDeferred counts rejections whose needs_reauth raise was held back because
	// the client did not yet know which stored session it was using (the next sync decides).
	ReauthRaiseDeferred prometheus.Counter
	// ReauthRaiseRefused counts needs_reauth raises refused because the pod that saw the rejection does not hold the user's group: reason
	// not_owner (raises are owner-only and no token) or fenced (it held the group and lost it). Both created at 0. Single-owner M4-3.
	ReauthRaiseRefused *prometheus.CounterVec
	// ReauthRaiseAttempts counts needs_reauth raises about to be written, by what they carry: token (the group's epoch: this pod owns the user),
	// unowned (the user has no group: nobody could own it, so the raise goes through, fence.Unowned) or none (no token: with RIVOLT_SINGLE_OWNER on
	// this is the raise refused as not_owner, so with it off this is the would-be refusal count). All three created at 0.
	ReauthRaiseAttempts *prometheus.CounterVec
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
	// Push sends by channel (web, apns), push service class (apple, fcm, mozilla, windows, other) and outcome (ok, gone, rejected, error).
	PushSendsTotal *prometheus.CounterVec

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
	// TireLowWouldSendTotal counts tire-low episodes that would have sent a push (the dry run), by the low word only.
	TireLowWouldSendTotal *prometheus.CounterVec
	// TripPlanUpstreamSeconds is how long a trip-planner call that reached the planner took (a leg-cache miss: the gateway on a live install,
	// the synthetic planner on a replay one), by outcome (ok|error). TripPlanLegsTotal counts every planner leg asked for by what happened:
	// hit (answered from the 15-minute leg cache), miss (sent to the planner), quota (refused: 60 an hour per user) or error. A multi-day
	// plan is one leg per day. Neither has a user, vehicle or place in a label.
	TripPlanUpstreamSeconds *prometheus.HistogramVec
	TripPlanLegsTotal       *prometheus.CounterVec
	// TripPlanStageSeconds times our own stages around a trip-planner call (a plan, or a day of a multi-day plan), by stage. No user, vehicle
	// or place in a label.
	TripPlanStageSeconds *prometheus.HistogramVec
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
	// AppleLinkTotal counts Link Apple to my account requests by outcome (see appleLinkOutcomes in internal/api).
	AppleLinkTotal *prometheus.CounterVec
	// RoutePointRefusedTotal counts route points kept out of a stored drive route, by where (see RoutePointRefusals).
	RoutePointRefusedTotal *prometheus.CounterVec
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
			// scale up. `status` is the numeric code ("200", "502"), NOT a
			// family: match it with status=~"5.." (an earlier note here said
			// "5xx", and alerts written to it never fired).
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
		TakeoverWaitSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rivolt_rivian_takeover_wait_seconds",
			Help:    "Seconds a takeover's Rivian call waited for its pacing slot, by class (dial, seed, features) and priority (active, idle).",
			Buckets: []float64{0.01, 0.1, 0.5, 1, 2, 5, 10, 30, 60, 300},
		}, []string{"class", "priority"}),
		TakeoverStarvedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_rivian_takeover_starved_total",
			Help: "Takeover calls that went ahead without a pacing slot after the wait bound (30 s active, 5 min idle).",
		}, []string{"class", "priority"}),
		TakeoverLocalOnlyTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_rivian_takeover_local_only_total",
			Help: "Takeover calls paced by the process's own bucket alone because the fleet budget in Redis did not answer.",
		}, []string{"class"}),
		SessionDecrypts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_rivian_session_decrypts_total",
			Help: "Decrypts of a stored Rivian session by call site (boot_sweep, lazy_restore, rehydrate, forward_adopt, sync, settings, request_hydrate, expiry, other).",
		}, []string{"site"}),
		TokensLoadedUnheld: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rivolt_tokens_loaded_unheld",
			Help: "Rivian session tokens held in this pod's memory for users whose group another pod holds (0 with lead mode off).",
		}),
		LeaseReconcileTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_lease_reconcile_total",
			Help: "Lease reconcile passes by trigger: start, tick, signal (a peer released groups) or other.",
		}, []string{"trigger"}),
		LeaseLeadPassSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "rivolt_lease_lead_pass_seconds",
			Help:    "Database time of one lead-mode group lease pass, 5 ms to 10 s.",
			Buckets: prometheus.DefBuckets,
		}),
		HeldGroupUsersSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "rivolt_held_group_users_seconds",
			Help:    "Time of the owner's read of the users in the groups this pod holds, 5 ms to 10 s.",
			Buckets: prometheus.DefBuckets,
		}),
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
		OpenCharges: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "rivolt_open_charges",
			Help: "Open live charge rows by state (charging or not_charging by the stale-charge janitor's rule, or no_frame: the vehicle sent nothing in 2 days); no vehicle or user labels.",
		}, []string{"state"}),
		HandoffGapSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rivolt_handoff_gap_seconds",
			Help:    "Seconds from the previous holder releasing a vehicle's group to the first frame on the new pod, by state at that frame (driving, charging, idle).",
			Buckets: []float64{2, 4, 6, 8, 10, 12, 15, 20, 30, 45, 60, 120},
		}, []string{"state"}),
		HandoffWaitSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "rivolt_handoff_wait_seconds",
			Help:    "Seconds a released vehicle group sat free before another pod took it (both ends on the database clock).",
			Buckets: []float64{0.5, 1, 2, 3, 4, 5, 6, 8, 10, 15, 30, 60},
		}),
		HandoffStopSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "rivolt_handoff_stop_seconds",
			Help:    "Seconds from this pod asking a vehicle's subscription to stop (to hand it over) to it being gone.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 3, 4, 5},
		}),
		RolloutHandoffsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_rollout_handoffs_total",
			Help: "Vehicles a pod took from another holder, by reason (drain, rebalance, expired, other).",
		}, []string{"reason"}),
		SessionRehydrateTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_session_rehydrate_total",
			Help: "Open drives and charges found at a takeover, by kind (drive, charge) and source (snapshot, db, fresh: nothing came back, a split).",
		}, []string{"kind", "source"}),
		SessionSplitTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_session_split_total",
			Help: "Drives and charges split in two because a takeover lost the open session, by kind (drive, charge).",
		}, []string{"kind"}),
		WeatherRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_weather_requests_total",
			Help: "Open-Meteo requests by endpoint (forecast, archive) and outcome (ok, error, skipped_old).",
		}, []string{"endpoint", "outcome"}),
		VehicleDiscoveryTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_vehicle_discovery_total",
			Help: "Vehicle discovery turns for linked users with no vehicle row, by outcome (stored, empty, error, skipped, needs_reauth).",
		}, []string{"outcome"}),
		VehiclesFallbackTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_vehicles_fallback_total",
			Help: "The linked-account-with-no-stored-vehicle Rivian lookups of /api/vehicles, by outcome (stored, empty, error, skipped, unsynced).",
		}, []string{"outcome"}),
		ChargewatchLastSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "rivolt_chargewatch_last_success_timestamp_seconds",
			Help: "Unix time of the last successful open-charge measurement (the rivolt_open_charges gauges keep their last values when a read fails).",
		}),
		OpenChargesOldestSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "rivolt_open_charges_oldest_seconds",
			Help: "Age since the last write of the oldest open live charge row, by state; 0 when there is none.",
		}, []string{"state"}),
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
		RivianRequestSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "rivolt_rivian_request_seconds",
			Help:    "Seconds each request to Rivian's GraphQL gateway took (sent to answered or failed), by operation.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 30},
		}, []string{"op"}),
		RivianWSReconnects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_rivian_ws_reconnects_total",
			Help: "vehicleState subscriptions opened again, by reason: nudge, watchdog, no_frames, error or clean.",
		}, []string{"reason"}),
		RivianCallRefused: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_rivian_call_refused_total",
				Help: "Rivian calls not made because this pod does not hold the vehicle's group lease, by call site. Non-zero is a bug finding.",
			},
			[]string{"site"},
		),
		RivianUnpermitted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_rivian_unpermitted_total",
			Help: "Requests to Rivian with neither a fence token nor a holder marker (RIVOLT_RIVIAN_PERMIT observe or enforce), by GraphQL operation. Non-zero is a bug finding.",
		}, []string{"op"}),
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
		LeaseUnstreamedVehicles: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "rivolt_lease_unstreamed_vehicles",
				Help: "Vehicles this pod holds a subscription lease for with no subscription running, as of the lease coordinator's last check.",
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
		ReauthRaiseRefused: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_reauth_raise_refused_total",
			Help: "needs_reauth raises refused because this pod does not hold the user's group (not_owner) or just lost it (fenced).",
		}, []string{"reason"}),
		ReauthRaiseAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "rivolt_reauth_raise_attempts_total",
			Help: "needs_reauth raises about to be written, by what they carry: token (this pod owns the user), unowned (the user has no group) or none (no token; refused as not_owner once raises are owner-only).",
		}, []string{"carries"}),
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
		RoutePointRefusedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_route_point_refused_total",
				Help: "Route points kept out of a stored drive route because they are not on the globe, by where.",
			},
			[]string{"where"},
		),
		AppleLinkTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_apple_link_total",
				Help: "Link Apple to my account by outcome.",
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
		PushSendsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_push_sends_total",
				Help: "Push sends by channel, push service and outcome (ok, gone, rejected, error).",
			},
			[]string{"channel", "service", "outcome"},
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
		TireLowWouldSendTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_tire_low_would_send_total",
				Help: "Tire-low episodes that would have sent a push while the real push is off (the dry run), by class (the truck's low word). Never a user, vehicle or wheel.",
			},
			[]string{"class"},
		),
		TripPlanUpstreamSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "rivolt_trip_plan_upstream_seconds",
				Help:    "Trip-planner calls that reached the planner (leg-cache misses), by outcome (ok|error).",
				Buckets: []float64{0.25, 0.5, 1, 2, 4, 6, 8, 10, 15, 20, 30, 45, 60},
			},
			[]string{"outcome"},
		),
		TripPlanStageSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "rivolt_trip_plan_stage_seconds",
				Help:    "Our own stages around a trip-planner call, by stage (geometry|enrich_load|enrich).",
				Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2, 4, 8},
			},
			[]string{"stage"},
		),
		TripPlanLegsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "rivolt_trip_plan_legs_total",
				Help: "Trip-planner legs asked for, by result (hit: leg cache; miss: sent to the planner; quota: refused; error).",
			},
			[]string{"result"},
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
		m.OpenCharges,
		m.OpenChargesOldestSeconds,
		m.ChargewatchLastSuccess,
		m.VehiclesFallbackTotal,
		m.VehicleDiscoveryTotal,
		m.WeatherRequestsTotal,
		m.HandoffGapSeconds,
		m.HandoffWaitSeconds,
		m.HandoffStopSeconds,
		m.RolloutHandoffsTotal,
		m.SessionRehydrateTotal,
		m.SessionSplitTotal,
		m.Accounts,
		m.BillingEnabled,
		m.LeaseAcquireToFrame,
		m.TakeoverWaitSeconds,
		m.TakeoverStarvedTotal,
		m.TakeoverLocalOnlyTotal,
		m.SessionDecrypts,
		m.TokensLoadedUnheld,
		m.LeaseReconcileTotal,
		m.LeaseLeadPassSeconds,
		m.HeldGroupUsersSeconds,
		m.LeaseFenceWouldReject,
		m.LeaseFenced,
		m.RivianCallRefused,
		m.RivianRequestSeconds,
		m.ReauthRaiseRefused,
		m.ReauthRaiseAttempts,
		m.RivianWSReconnects,
		m.RivianUnpermitted,
		m.RivianScheduleReadTotal,
		m.LeaseShed,
		m.LeaseUnstreamed,
		m.LeaseUnstreamedVehicles,
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
		m.AppleLinkTotal,
		m.RoutePointRefusedTotal,
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
		m.PushSendsTotal,
		m.PasskeyRefusedTotal,
		m.LogoutForgetAttemptsTotal,
		m.LogoutForgetFailuresTotal,
		m.OTAStatusFramesTotal,
		m.TireStatusTotal,
		m.TireLowWouldSendTotal,
		m.TripPlanUpstreamSeconds,
		m.TripPlanLegsTotal,
		m.TripPlanStageSeconds,
		m.RequestAcquiresWithoutUser,
	)
	// The lease counters exist from boot at 0, so the first fenced write or the first unmatched source
	// is an increase (a series that first appears at 1 has increase() == 0, and the alert would miss the
	// very event it is for). Keep the label values in step with the callers.
	for _, outcome := range applebill.Outcomes {
		m.AppleNotificationsTotal.WithLabelValues(outcome).Add(0)
	}
	for _, where := range RoutePointRefusals {
		m.RoutePointRefusedTotal.WithLabelValues(where).Add(0)
	}
	for _, outcome := range AppleLinkOutcomes {
		m.AppleLinkTotal.WithLabelValues(outcome).Add(0)
	}
	for _, stage := range TripPlanStages { // the series exist from boot
		m.TripPlanStageSeconds.WithLabelValues(stage)
	}
	for route, statuses := range BillingRouteStatuses { // the first error on a fresh pod is an increase (see BillingRouteStatuses)
		for _, status := range statuses {
			m.HTTPRequestsTotal.WithLabelValues("POST", route, status).Add(0)
		}
	}
	for _, result := range TripPlanLegResults { // the first quota refusal or error is an increase
		m.TripPlanLegsTotal.WithLabelValues(result).Add(0)
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
	for _, site := range SessionDecryptSites {
		m.SessionDecrypts.WithLabelValues(site).Add(0)
	}
	m.TokensLoadedUnheld.Set(0)
	for _, class := range TakeoverClasses {
		m.TakeoverLocalOnlyTotal.WithLabelValues(class)
		for _, prio := range []string{"active", "idle"} {
			m.TakeoverWaitSeconds.WithLabelValues(class, prio)
			m.TakeoverStarvedTotal.WithLabelValues(class, prio).Add(0)
		}
	}
	for _, tr := range []string{"start", "tick", "signal", "other"} {
		m.LeaseReconcileTotal.WithLabelValues(tr).Add(0)
	}
	for _, op := range RivianPermitOps {
		if op != "ws_dial" {
			m.RivianRequestSeconds.WithLabelValues(op) // the child exists at zero observations from boot
		}
	}
	for _, reason := range []string{"not_owner", "fenced"} {
		m.ReauthRaiseRefused.WithLabelValues(reason).Add(0)
	}
	for _, c := range []string{"token", "unowned", "none"} {
		m.ReauthRaiseAttempts.WithLabelValues(c).Add(0)
	}
	for _, reason := range RivianWSReconnectReasons {
		m.RivianWSReconnects.WithLabelValues(reason).Add(0)
	}
	for _, op := range RivianPermitOps {
		m.RivianUnpermitted.WithLabelValues(op).Add(0)
	}
	for _, site := range RivianCallSites {
		m.RivianCallRefused.WithLabelValues(site).Add(0) // exported at 0 so the first refusal is an increase
	}
	for _, class := range []string{"priority", "main", "background"} {
		m.RivianRateLimitBlocked.WithLabelValues(class).Add(0) // exported at 0 so the first refusal is an increase
	}
	for _, outcome := range []string{"ok", "retried_ok", "dropped", "error"} {
		m.RivianScheduleReadTotal.WithLabelValues(outcome).Add(0)
	}
	for _, state := range []string{"driving", "charging", "idle"} {
		m.HandoffGapSeconds.WithLabelValues(state) // exported at count 0 from boot
	}
	for _, reason := range []string{"drain", "rebalance", "expired", "other"} {
		m.RolloutHandoffsTotal.WithLabelValues(reason).Add(0)
	}
	for _, kind := range []string{"drive", "charge"} {
		m.SessionSplitTotal.WithLabelValues(kind).Add(0)
		for _, source := range []string{"snapshot", "db", "fresh"} {
			m.SessionRehydrateTotal.WithLabelValues(kind, source).Add(0)
		}
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
// RivianCallSites are the label values of rivolt_rivian_call_refused_total; rivian.CallSites must list the same (a test in cmd/rivolt).
var RivianCallSites = []string{"seed", "refresh_once", "janitor_confirm", "wake_safety", "ws_dial", "live_session", "subscription", "schedule", "start"}

// SessionDecryptSites are the label values of rivolt_rivian_session_decrypts_total: the places that decrypt a stored Rivian session. Fixed, so
// the label never carries a user. A decrypt from anywhere else counts as "other".
var SessionDecryptSites = []string{"boot_sweep", "lazy_restore", "rehydrate", "forward_adopt", "discovery", "sync", "settings", "request_hydrate", "expiry", "other"}

// TakeoverClasses are the label values of the takeover pacing metrics; the call sites in rivian.StateMonitor use the same names.
var TakeoverClasses = []string{"dial", "seed", "features"}

// RivianPermitOps are the label values of rivolt_rivian_unpermitted_total; rivian.PermitOps must list the same (a test in cmd/rivolt).
var RivianPermitOps = []string{"__Introspect", "CreateCSRFToken", "GetChargingSchedule", "GetCompletedSessionSummaries", "GetDetailedSessionInfoByTxnId",
	"getLiveSessionHistory", "getOTAUpdateDetails", "getUserInfo", "getVehicleImages", "GetVehicleState", "IntrospectEnum", "IntrospectInput", "Login",
	"LoginWithOTP", "planTripWithMultiStop", "planTripWithMultiStopV2", "Probe", "SupportedFeatures", "ws_dial", "other"}

// RivianWSReconnectReasons are the label values of rivolt_rivian_ws_reconnects_total; rivian.WSReconnectReasons must list the same (a test in cmd/rivolt).
var RivianWSReconnectReasons = []string{"nudge", "watchdog", "no_frames", "error", "clean"}

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
