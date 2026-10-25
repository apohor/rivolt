# Row-level security: requests that reach the database with no user

With `RIVOLT_REQUEST_DATABASE_URL` set, request handling uses a database role that row-level security applies to. Every connection it
hands out is scoped to the signed-in user (`app.user_id`). A request that reaches the database **without** a user gets a connection
scoped to nobody: every per-user read returns zero rows. It fails closed (no leak), but to a person it is an empty page, not an error.
The counter below is how we find those routes before a person does.

## The signal

`rivolt_request_acquires_without_user_total{route}` counts request-pool connections handed out for a context with no user. `route` is
the chi route pattern (`/api/vehicles/{vehicleID}/now`), never the path. `none` is a goroutine that is not a request (a job); `unmatched`
is a 404. With the request pool off (the default, and every self-hosted install) the counter stays at zero: it only means something
once `RIVOLT_REQUEST_DATABASE_URL` is set.

Because a series only exists after its first increment, an alert on `increase()` alone misses a route's first hit. Use:

```promql
# routes that took a user-less connection in the last 15 minutes, including a route's very first one
(increase(rivolt_request_acquires_without_user_total{route!="none"}[15m]) > 0)
  or (rivolt_request_acquires_without_user_total{route!="none"} unless rivolt_request_acquires_without_user_total{route!="none"} offset 15m)
```

The numbers that go with it, per route, for the stage-3 soak (the pool must not make anything slower than the `set_config` round trip):

```promql
histogram_quantile(0.5,  sum by (le, route) (rate(rivolt_http_request_duration_seconds_bucket[10m])))
histogram_quantile(0.95, sum by (le, route) (rate(rivolt_http_request_duration_seconds_bucket[10m])))
```

## When it is non-zero

1. **Find the route.** The `route` label names it. `none` is expected from jobs and is filtered out above.
2. **Is it a signed-in user's page?** Reproduce it as a signed-in user on preview. An empty answer where there should be data is the
   symptom.
3. **Decide which of two it is:**
   - *The route is a system path* (login, session or token lookup, the app token exchange, signup, account deletion, an admin route, a
     signed-link endpoint, `/api/config`, anything before there is a user). It belongs on `Deps.SystemDB`. Move the read there, with a
     comment saying why, and DA's ack in that PR (policy: `docs/` S2-3 list in the RLS stage-2 plan).
   - *The route is a user path that lost its user.* The usual cause is a query made on `context.Background()`, or on a context that is
     not derived from the request (a goroutine started from a handler, a cache fill). Pass the request's context (use
     `context.WithoutCancel(r.Context())` to outlive the request: it keeps the user).
4. **Never** make the request role bypass RLS, grant it `BYPASSRLS`, or point the request pool at the owner to quiet the counter. The
   boot check refuses a bypassing role for this reason.
5. If a route truly must take a user-less connection, list it in `noUserAcquireAllowed` in `internal/api/pool_on_integration_test.go` with
   the reason, so the integration run (which fails on any such route) stays green on purpose, not by accident.

## In CI

The integration job runs the API tests with a real request pool (`RIVOLT_TEST_REQUEST_POOL=1`). The test harness counts user-less acquires
by route the same way the metric does and **fails the test** naming the route. A new handler that reads per-user data without a user
therefore fails in CI, before it reaches preview.

## Turning the request pool off in an emergency

Unset `RIVOLT_REQUEST_DATABASE_URL` and restart: the request pool becomes the system pool again (behaviour as before row-level security).
Do this only to restore service; the empty pages the counter reports are the thing to fix.
