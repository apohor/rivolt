import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type AdminFlagsState, type AdminUserRow, type SignupRequest } from "../lib/api";
import { grafanaBaseURL } from "../lib/config";
import { Card, clickableRowProps, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { AIProvidersPanel, RecapWeatherPanel, GPSAccuracyPanel } from "./SettingsPage";

// AdminPage is gated server-side by requireAdminMW + client-side by
// the AppLayout nav check. We still defensively render an
// "unauthorized" message if a non-admin reaches this route directly
// (e.g. by typing /admin into the URL); the API calls would 403
// anyway but the empty-state is more honest than a list-of-errors.
export default function AdminPage() {
  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => backend.whoami(),
    staleTime: 5 * 60_000,
  });
  if (me.isLoading) return <Spinner />;
  if (!me.data || me.data.role !== "admin") {
    return (
      <div className="space-y-4">
        <PageHeader title="Admin" />
        <Card title="Not authorized">
          <p className="text-sm text-neutral-400">
            This page is only available to administrators.
          </p>
        </Card>
      </div>
    );
  }
  return (
    <div className="space-y-4">
      <PageHeader title="Admin" />
      <AdminTabs currentUserID={me.data.user_id} />
    </div>
  );
}

// grafanaUserExploreURL builds Grafana Explore deep links scoped
// to one user_id over a time window. Returns "" when no Grafana
// origin is wired so callers fall back to plain text.
//
// We emit two flavours of link:
//   - logs:   {namespace=~"rivolt.*"} |~ "<uid>" against Loki —
//             matches both server slog lines and any free-form
//             mention of the uid.
//   - traces: { .user.id = "<uid>" } against Tempo TraceQL —
//             relies on the user.id span attribute stamped in
//             auth.WithUser (v0.18.69+). Older spans won't match.
function grafanaUserExploreURL(
  userID: string,
  fromISO: string,
  toISO: string,
  kind: "logs" | "traces" = "logs",
): string {
  const base = grafanaBaseURL();
  if (!base) return "";
  const from = new Date(fromISO).getTime();
  const to = new Date(toISO).getTime();
  if (!Number.isFinite(from) || !Number.isFinite(to)) return "";
  // Grafana 11+ replaced the `?left=` URL param with a new
  // `?panes=<encoded>&schemaVersion=1` shape keyed by a
  // randomly-generated pane id. The legacy `left` param is
  // still parsed but the datasource field gets dropped on the
  // floor — Grafana then defaults to the first provisioned
  // datasource of any type (Tempo, in our case), which is the
  // "logs link opens Tempo" symptom. Build the new format.
  const ds =
    kind === "logs"
      ? { type: "loki", uid: "loki" }
      : { type: "tempo", uid: "tempo" };
  const query =
    kind === "logs"
      ? {
          refId: "A",
          datasource: ds,
          expr: `{namespace=~"rivolt.*"} |~ "${userID}"`,
        }
      : {
          refId: "A",
          datasource: ds,
          queryType: "traceql",
          query: `{ .user.id = "${userID}" }`,
        };
  const pane = {
    datasource: ds.uid,
    queries: [query],
    range: { from: String(from), to: String(to) },
  };
  // Pane id is an arbitrary short key Grafana uses to identify
  // each split. Keep it deterministic per-kind for predictable
  // URLs but it could be any string.
  const paneID = kind === "logs" ? "lg" : "tr";
  const panes = { [paneID]: pane };
  return `${base}/explore?schemaVersion=1&orgId=1&panes=${encodeURIComponent(JSON.stringify(panes))}`;
}

// AdminTabs groups admin-only surfaces under a single tab nav. The
// active tab is mirrored in the URL hash so a reload (or a copy-
// pasted link to /admin#ai) keeps the position the operator was on.
type AdminTab = "users" | "signups" | "ai" | "operations" | "tuning";

// Signups is first because pending waitlist requests are the
// most time-sensitive thing on this page — every other tab is
// reactive ops (a user reported X) or background tuning.
const ADMIN_TABS: { id: AdminTab; label: string }[] = [
  { id: "signups", label: "Signups" },
  { id: "users", label: "Users" },
  { id: "ai", label: "AI" },
  { id: "operations", label: "Operations" },
  { id: "tuning", label: "Tuning" },
];

function isAdminTab(s: string): s is AdminTab {
  return ADMIN_TABS.some((t) => t.id === s);
}

function AdminTabs({ currentUserID }: { currentUserID: string }) {
  const initial: AdminTab = (() => {
    const h = window.location.hash.replace(/^#/, "");
    return isAdminTab(h) ? h : "signups";
  })();
  const [tab, setTab] = useState<AdminTab>(initial);
  // Keep the hash in sync so a reload restores the active tab and
  // a deep link (`/admin#ai`) opens the right pane. Listen for
  // hashchange too — the back button moves between tabs cleanly.
  useEffect(() => {
    if (window.location.hash.replace(/^#/, "") !== tab) {
      window.history.replaceState(null, "", `#${tab}`);
    }
  }, [tab]);
  useEffect(() => {
    const onHash = () => {
      const h = window.location.hash.replace(/^#/, "");
      if (isAdminTab(h)) setTab(h);
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-1 border-b border-neutral-800">
        {ADMIN_TABS.map((t) => {
          const active = t.id === tab;
          return (
            <button
              key={t.id}
              type="button"
              onClick={() => setTab(t.id)}
              className={`-mb-px border-b-2 px-3 py-1.5 text-sm transition-colors ${
                active
                  ? "border-emerald-500 text-neutral-100"
                  : "border-transparent text-neutral-500 hover:text-neutral-300"
              }`}
            >
              {t.label}
            </button>
          );
        })}
      </div>
      {tab === "users" && (
        <Card title="Users">
          <CreateUserForm />
          <UsersPanel currentUserID={currentUserID} />
        </Card>
      )}
      {tab === "signups" && (
        <Card title="Signup requests">
          <SignupRequestsPanel />
        </Card>
      )}
      {tab === "ai" && (
        <Card title="AI providers">
          <AIProvidersPanel />
        </Card>
      )}
      {tab === "operations" && (
        <div className="space-y-4">
          <Card title="Feature flags">
            <FeatureFlagsPanel />
          </Card>
          <Card title="Signup cap">
            <SignupCapPanel />
          </Card>
        </div>
      )}
      {tab === "tuning" && (
        <div className="space-y-4">
          <Card title="Recap weather">
            <RecapWeatherPanel />
          </Card>
          <Card title="GPS accuracy thresholds">
            <GPSAccuracyPanel />
          </Card>
        </div>
      )}
    </div>
  );
}

// FeatureFlagsPanel exposes the install-wide operational flags
// (Rivian-upstream kill switch + trip-planner feature flag) so the
// operator can flip them without psql or a deploy. Both are polled
// server-side at ~10s; the local pod sees the flip immediately, peers
// catch up on their next refresh.
function FeatureFlagsPanel() {
  const qc = useQueryClient();
  const flagsQ = useQuery({
    queryKey: ["admin", "flags"],
    queryFn: backend.adminFlagsGet,
  });

  const [reason, setReason] = useState("");

  const killMut = useMutation({
    mutationFn: ({ paused, reason }: { paused: boolean; reason: string }) =>
      backend.adminFlagsKillPut(paused, reason),
    onSuccess: (data) => {
      // Merge into the cached AdminFlagsState rather than overwriting
      // — the kill-switch PUT only echoes its own field on the wire.
      qc.setQueryData(["admin", "flags"], (prev: typeof data | undefined) => ({
        kill_switch: data.kill_switch,
        trip_planner: prev?.trip_planner ?? { enabled: false },
      }));
    },
  });
  const plannerMut = useMutation({
    mutationFn: (enabled: boolean) => backend.adminFlagsTripPlannerPut(enabled),
    onSuccess: (data) => {
      qc.setQueryData(["admin", "flags"], (prev: AdminFlagsState | undefined) => ({
        kill_switch: prev?.kill_switch ?? { paused: false },
        trip_planner: data.trip_planner,
      }));
    },
  });

  if (flagsQ.isLoading) return <Spinner />;
  if (flagsQ.isError)
    return (
      <ErrorBox title="Failed to load flags" detail={(flagsQ.error as Error).message} />
    );
  const f = flagsQ.data!;

  return (
    <div className="space-y-4 text-sm">
      <div className="flex items-baseline justify-between gap-3">
        <div>
          <div className="text-neutral-100">Rivian upstream kill switch</div>
          <p className="text-xs text-neutral-500">
            Pauses every outbound Rivian call across all users.{" "}
            {f.kill_switch.paused
              ? `Currently PAUSED${f.kill_switch.reason ? ` — ${f.kill_switch.reason}` : ""}.`
              : "Upstream calls flowing normally."}
          </p>
        </div>
        <button
          type="button"
          disabled={killMut.isPending}
          onClick={() =>
            killMut.mutate({
              paused: !f.kill_switch.paused,
              reason: f.kill_switch.paused ? "" : reason,
            })
          }
          className={`shrink-0 rounded-md border px-3 py-1.5 text-xs ${
            f.kill_switch.paused
              ? "border-emerald-700 bg-emerald-900/40 text-emerald-200 hover:bg-emerald-900/60"
              : "border-rose-700 bg-rose-900/40 text-rose-200 hover:bg-rose-900/60"
          } disabled:opacity-50`}
        >
          {f.kill_switch.paused ? "Resume" : "Pause"}
        </button>
      </div>
      {!f.kill_switch.paused && (
        <input
          type="text"
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder="Reason (recorded with the pause for future operators)"
          className="w-full rounded-md border border-neutral-700 bg-neutral-900 px-2.5 py-1.5 text-xs text-neutral-100 placeholder:text-neutral-500 focus:border-neutral-500 focus:outline-hidden"
        />
      )}

      <div className="border-t border-neutral-800 pt-3 flex items-baseline justify-between gap-3">
        <div>
          <div className="text-neutral-100">Trip planner</div>
          <p className="text-xs text-neutral-500">
            When off, the Plan nav link is hidden and /api/trips/* + the
            planner settings 404. Useful while the feature is still
            iterating.
          </p>
        </div>
        <button
          type="button"
          disabled={plannerMut.isPending}
          onClick={() => plannerMut.mutate(!f.trip_planner.enabled)}
          className={`shrink-0 rounded-md border px-3 py-1.5 text-xs ${
            f.trip_planner.enabled
              ? "border-rose-700 bg-rose-900/40 text-rose-200 hover:bg-rose-900/60"
              : "border-emerald-700 bg-emerald-900/40 text-emerald-200 hover:bg-emerald-900/60"
          } disabled:opacity-50`}
        >
          {f.trip_planner.enabled ? "Disable" : "Enable"}
        </button>
      </div>

      {(killMut.isError || plannerMut.isError) && (
        <p className="text-xs text-rose-400">
          Save failed:{" "}
          {String(
            (killMut.error as Error)?.message ??
              (plannerMut.error as Error)?.message,
          )}
        </p>
      )}
    </div>
  );
}


// SignupCapPanel gates new OAuth signups by total account count.
// Existing users signing back in are always exempt; the limit only
// caps new-row creation in the OIDC callback. Limit=0 fail-closes
// every new signup, which is correct when the operator wants to
// stop letting people in.
function SignupCapPanel() {
  const qc = useQueryClient();
  const capQ = useQuery({
    queryKey: ["admin", "signup-cap"],
    queryFn: backend.adminSignupCapGet,
  });
  const [draft, setDraft] = useState<string>("");
  const saveMut = useMutation({
    mutationFn: (limit: number) => backend.adminSignupCapPut(limit),
    onSuccess: (data) => {
      qc.setQueryData(["admin", "signup-cap"], data);
      setDraft("");
    },
  });

  if (capQ.isLoading) return <Spinner />;
  if (capQ.isError)
    return (
      <ErrorBox
        title="Failed to load signup cap"
        detail={(capQ.error as Error).message}
      />
    );
  const c = capQ.data!;
  const current = c.signup_cap.limit;
  const used = c.used;
  const headroom = Math.max(0, current - used);
  const parsed = draft.trim() === "" ? null : Number(draft);
  const valid = parsed !== null && Number.isFinite(parsed) && parsed >= 0 && Number.isInteger(parsed);

  return (
    <div className="space-y-3 text-sm">
      <div>
        <div className="text-neutral-100">
          {used} of {current} seats used
          {headroom === 0 && current > 0 ? " (full)" : ""}
        </div>
        <p className="text-xs text-neutral-500">
          New OAuth signups are blocked once total accounts reach the
          limit; users already on the system can always sign back in.
          A limit of 0 stops every new signup. Effective within ~10s.
        </p>
      </div>
      <div className="flex items-center gap-2">
        <input
          type="number"
          min={0}
          inputMode="numeric"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={String(current)}
          className="w-32 rounded-md border border-neutral-700 bg-neutral-900 px-2.5 py-1.5 text-xs text-neutral-100 placeholder:text-neutral-500 focus:border-neutral-500 focus:outline-hidden"
        />
        <button
          type="button"
          disabled={!valid || saveMut.isPending || parsed === current}
          onClick={() => valid && saveMut.mutate(parsed)}
          className="shrink-0 rounded-md border border-emerald-700 bg-emerald-900/40 px-3 py-1.5 text-xs text-emerald-200 hover:bg-emerald-900/60 disabled:opacity-50"
        >
          {saveMut.isPending ? "Saving…" : "Save"}
        </button>
      </div>
      {saveMut.isError && (
        <p className="text-xs text-rose-400">
          Save failed: {String((saveMut.error as Error)?.message)}
        </p>
      )}
    </div>
  );
}

function CreateUserForm() {
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [role, setRole] = useState<"user" | "admin">("user");
  const [disabled, setDisabled] = useState(false);
  // One-time password surfaced when the server's IdP (Kratos)
  // integration is wired in. Cleared by the next form submit
  // and on dismiss — never persisted, never refetched. Admin
  // must copy it out before the dialog closes.
  const [oneTimePassword, setOneTimePassword] = useState<{
    username: string;
    password: string;
  } | null>(null);

  const create = useMutation({
    mutationFn: () =>
      backend.adminCreateUser({
        email: email.trim(),
        display_name: displayName.trim() || undefined,
        role,
        disabled: disabled || undefined,
      }),
    onSuccess: (resp) => {
      const created = email.trim();
      setEmail("");
      setDisplayName("");
      setRole("user");
      setDisabled(false);
      qc.invalidateQueries({ queryKey: ["admin", "users"] });
      if (resp.password) {
        setOneTimePassword({ username: created, password: resp.password });
      }
    },
  });

  // Email is the canonical Kratos credential identifier. Display
  // name is optional — the server defaults it to the email when
  // empty. Role and disabled stay explicit because their defaults
  // matter operationally (a stale "admin" toggle would be a bug).
  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        if (!email.trim()) return;
        create.mutate();
      }}
      className="mb-4 grid grid-cols-1 gap-2 rounded-md border border-neutral-800 bg-neutral-950 p-3 sm:grid-cols-5"
    >
      <input
        type="email"
        required
        placeholder="email"
        value={email}
        onChange={(e) => setEmail(e.target.value)}
        className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
      />
      <input
        type="text"
        placeholder="display name (optional)"
        value={displayName}
        onChange={(e) => setDisplayName(e.target.value)}
        className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
      />
      <select
        value={role}
        onChange={(e) => setRole(e.target.value as "user" | "admin")}
        className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
      >
        <option value="user">user</option>
        <option value="admin">admin</option>
      </select>
      <label className="flex items-center gap-2 rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-sm text-neutral-300">
        <input
          type="checkbox"
          checked={disabled}
          onChange={(e) => setDisabled(e.target.checked)}
          className="h-3.5 w-3.5"
        />
        disabled
      </label>
      <button
        type="submit"
        disabled={!email.trim() || create.isPending}
        className="rounded-md border border-emerald-800 bg-emerald-950 px-3 py-1 text-sm text-emerald-200 hover:border-emerald-700 hover:text-emerald-100 disabled:opacity-40"
      >
        {create.isPending ? "Creating…" : "Create user"}
      </button>
      <p className="col-span-full text-xs text-neutral-500">
        Provisions the user in Kratos with a one-time password. They
        sign in at <span className="font-mono">auth.rivolt.dev</span>{" "}
        with this email and the password shown below, then change it
        via the IdP self-service flow.
      </p>
      {create.error && (
        <p className="col-span-full text-xs text-red-400">
          {String(create.error)}
        </p>
      )}
      {oneTimePassword && (
        <div className="col-span-full rounded-md border border-amber-800 bg-amber-950/40 p-3 text-xs text-amber-100">
          <p className="mb-2 font-medium">
            One-time password for{" "}
            <span className="font-mono">{oneTimePassword.username}</span>
          </p>
          <div className="flex items-center gap-2">
            <code className="select-all rounded-sm bg-neutral-900 px-2 py-1 font-mono text-sm text-amber-200">
              {oneTimePassword.password}
            </code>
            <button
              type="button"
              onClick={() => {
                navigator.clipboard?.writeText(oneTimePassword.password);
              }}
              className="rounded-md border border-amber-800 px-2 py-1 text-amber-200 hover:border-amber-700"
            >
              Copy
            </button>
            <button
              type="button"
              onClick={() => setOneTimePassword(null)}
              className="ml-auto rounded-md border border-neutral-700 px-2 py-1 text-neutral-300 hover:border-neutral-500"
            >
              Dismiss
            </button>
          </div>
          <p className="mt-2 text-amber-200/80">
            Send this to the user out-of-band. It is shown once and is
            not stored. The user can change it after first sign-in.
          </p>
        </div>
      )}
    </form>
  );
}

// relativeTime renders a short "Xm ago" / "Xh ago" / "Xd ago" string
// from an ISO timestamp. Used in the admin users table where seeing
// "5m ago" vs "3d ago" is more useful than the full datetime.
function relativeTime(iso: string): string {
  const then = new Date(iso).getTime();
  if (isNaN(then)) return "—";
  const diff = Math.floor((Date.now() - then) / 1000);
  if (diff < 60) return `${diff}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return `${Math.floor(diff / 86400)}d ago`;
}

function UsersPanel({ currentUserID }: { currentUserID: string }) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["admin", "users"],
    queryFn: () => backend.adminListUsers(),
  });
  const setRole = useMutation({
    mutationFn: ({ id, role }: { id: string; role: "user" | "admin" }) =>
      backend.adminSetUserRole(id, role),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "users"] }),
  });
  const del = useMutation({
    mutationFn: (id: string) => backend.adminDeleteUser(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "users"] }),
  });
  const setDisabled = useMutation({
    mutationFn: ({ id, disabled }: { id: string; disabled: boolean }) =>
      backend.adminSetUserDisabled(id, disabled),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "users"] }),
  });
  const syncRivian = useMutation({
    mutationFn: (id: string) => backend.adminSyncRivian(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "users"] }),
  });
  const refreshRivianSession = useMutation({
    mutationFn: (id: string) => backend.adminRefreshRivianSession(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "users"] }),
  });

  const rows: AdminUserRow[] = useMemo(() => q.data?.users ?? [], [q.data]);
  const [busyID, setBusyID] = useState<string | null>(null);
  // The "demote / delete the last admin" guard exists server-side
  // (returns 409). The client mirrors it as a button disable so
  // the destructive action doesn't even appear clickable.
  const adminCount = rows.filter((u) => u.role === "admin" && !u.disabled).length;

  const [selectedID, setSelectedID] = useState<string | null>(null);
  // Auto-select first row so the detail panel has something to render
  // on initial load — picking explicitly afterwards just swaps the
  // selection. The deselect-on-delete effect below clears this if
  // the selected user disappears.
  useEffect(() => {
    if (selectedID == null && rows.length > 0) {
      setSelectedID(rows[0].id);
    } else if (selectedID != null && !rows.find((u) => u.id === selectedID)) {
      setSelectedID(rows[0]?.id ?? null);
    }
  }, [rows, selectedID]);

  if (q.isLoading) return <Spinner />;
  if (q.isError)
    return <ErrorBox title="Failed to load users" detail={String(q.error)} />;

  const selectedRow = rows.find((u) => u.id === selectedID) ?? null;
  const isLastAdminFor = (u: AdminUserRow) =>
    u.role === "admin" && adminCount <= 1;

  return (
    <div className="space-y-4">
      {/* Master list — full-width row of summary columns. Click a
          row to populate the detail panel below; the active row is
          tinted so the selection is obvious. */}
      <div className="rounded-md border border-neutral-900">
        <table className="w-full text-sm">
          <thead className="text-left text-neutral-500">
            <tr>
              <th className="py-2 px-3">User</th>
              <th className="py-2 px-3 hidden md:table-cell">Email</th>
              <th className="py-2 px-3">Role</th>
              <th className="py-2 px-3 hidden sm:table-cell">Activity</th>
              <th className="py-2 px-3 hidden sm:table-cell">Last seen</th>
              <th className="py-2 px-3 hidden lg:table-cell">Created</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((u) => {
              const active = u.id === selectedID;
              return (
                <tr
                  key={u.id}
                  aria-pressed={active}
                  {...clickableRowProps(() => setSelectedID(u.id), {
                    label: `Select ${u.display_name || u.username}`,
                  })}
                  className={`cursor-pointer border-t border-neutral-900 focus:outline-hidden focus-visible:bg-neutral-800/80 ${
                    active
                      ? "bg-emerald-950/30"
                      : "hover:bg-neutral-900/50"
                  }`}
                >
                  <td className="py-2 px-3">
                    <div className="text-neutral-100">
                      {u.display_name || u.username}
                    </div>
                    {/* Username/email under the display name. On
                        narrow viewports we collapse Email into this
                        sub-line so the operator still sees how to
                        contact the user without horizontal scroll. */}
                    <div className="text-xs text-neutral-500 md:hidden">
                      {u.email || u.username}
                    </div>
                    <div className="text-xs text-neutral-500 hidden md:block">
                      {u.username}
                    </div>
                  </td>
                  <td className="py-2 px-3 text-neutral-400 hidden md:table-cell">
                    {u.email || "—"}
                  </td>
                  <td className="py-2 px-3">
                    <span
                      className={`rounded-full border px-2 py-0.5 text-xs ${
                        u.role === "admin"
                          ? "border-emerald-700 bg-emerald-950 text-emerald-300"
                          : "border-neutral-800 bg-neutral-900 text-neutral-400"
                      }`}
                    >
                      {u.role}
                    </span>
                    {u.disabled && (
                      <span className="ml-1 rounded-full border border-amber-800 bg-amber-950 px-2 py-0.5 text-xs text-amber-300">
                        disabled
                      </span>
                    )}
                  </td>
                  <td className="py-2 px-3 hidden sm:table-cell">
                    <div className="flex flex-wrap gap-1 text-[10px]">
                      <span
                        className={
                          u.rivian_connected
                            ? "rounded-sm border border-emerald-800 bg-emerald-950 px-1.5 py-0.5 text-emerald-300"
                            : "rounded-sm border border-neutral-800 bg-neutral-900 px-1.5 py-0.5 text-neutral-500"
                        }
                      >
                        {u.rivian_connected ? "rivian" : "no rivian"}
                      </span>
                      {u.needs_reauth && (
                        <span
                          className="rounded-sm border border-rose-800 bg-rose-950 px-1.5 py-0.5 text-rose-300"
                          title={
                            u.needs_reauth_at
                              ? `Rivian session rejected ${relativeTime(u.needs_reauth_at)} - drives stopped recording`
                              : "Rivian session rejected - drives stopped recording"
                          }
                        >
                          re-auth
                        </span>
                      )}
                      <span className="rounded-sm border border-neutral-800 bg-neutral-900 px-1.5 py-0.5 text-neutral-400">
                        🚗 {u.vehicle_count}
                      </span>
                      <span className="rounded-sm border border-neutral-800 bg-neutral-900 px-1.5 py-0.5 text-neutral-400">
                        ↻ {u.drive_count}
                      </span>
                      <span className="rounded-sm border border-neutral-800 bg-neutral-900 px-1.5 py-0.5 text-neutral-400">
                        ⬇ {u.import_count}
                      </span>
                    </div>
                  </td>
                  <td
                    className="py-2 px-3 text-xs text-neutral-500 hidden sm:table-cell"
                    title={u.last_seen_at ?? ""}
                  >
                    {u.last_seen_at
                      ? relativeTime(u.last_seen_at)
                      : <span className="text-neutral-700">never</span>}
                  </td>
                  <td className="py-2 px-3 text-xs text-neutral-500 hidden lg:table-cell">
                    {new Date(u.created_at).toLocaleDateString()}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>

      {/* Detail panel — full-width below the list so columns aren't
          fighting for screen space. Fetches the deep-dive bundle
          for the selected row and holds every per-user action. */}
      <div>
        {selectedRow ? (
          <UserDetailPanel
            row={selectedRow}
            currentUserID={currentUserID}
            isLastAdmin={isLastAdminFor(selectedRow)}
            busy={busyID === selectedRow.id}
            onPromote={async () => {
              setBusyID(selectedRow.id);
              try {
                await setRole.mutateAsync({ id: selectedRow.id, role: "admin" });
              } finally {
                setBusyID(null);
              }
            }}
            onDemote={async () => {
              setBusyID(selectedRow.id);
              try {
                await setRole.mutateAsync({ id: selectedRow.id, role: "user" });
              } finally {
                setBusyID(null);
              }
            }}
            onToggleDisabled={async () => {
              setBusyID(selectedRow.id);
              try {
                await setDisabled.mutateAsync({
                  id: selectedRow.id,
                  disabled: !selectedRow.disabled,
                });
              } finally {
                setBusyID(null);
              }
            }}
            onSyncRivian={async () => {
              setBusyID(selectedRow.id);
              try {
                const r = await syncRivian.mutateAsync(selectedRow.id);
                alert(
                  `Synced — ${r.vehicle_count} vehicle${r.vehicle_count === 1 ? "" : "s"} on file for ${selectedRow.username}.`,
                );
              } catch (e) {
                alert(`Sync failed: ${String(e)}`);
              } finally {
                setBusyID(null);
              }
            }}
            onRefreshRivianSession={async () => {
              setBusyID(selectedRow.id);
              try {
                const r = await refreshRivianSession.mutateAsync(
                  selectedRow.id,
                );
                alert(r.message);
              } catch (e) {
                alert(`Refresh failed: ${String(e)}`);
              } finally {
                setBusyID(null);
              }
            }}
            onDelete={async () => {
              if (
                !confirm(
                  `Permanently delete ${selectedRow.username}? Their drives, charges, and settings will be removed.`,
                )
              )
                return;
              setBusyID(selectedRow.id);
              try {
                await del.mutateAsync(selectedRow.id);
              } finally {
                setBusyID(null);
              }
            }}
          />
        ) : (
          <div className="rounded-md border border-neutral-900 bg-neutral-950/40 px-4 py-6 text-sm text-neutral-500">
            Select a user to see details.
          </div>
        )}
        {(setRole.error || del.error) && (
          <p className="mt-2 text-xs text-red-400">
            {String(setRole.error ?? del.error)}
          </p>
        )}
      </div>
    </div>
  );
}

// UserDetailPanel renders the deep-dive bundle for one selected user
// plus every per-user action (promote/demote, enable/disable, sync
// rivian, delete). Fetches the bundle on each selectedID change so
// switching users feels live; row data from the master list paints
// immediately while the detailed counters load.
function UserDetailPanel({
  row,
  currentUserID,
  isLastAdmin,
  busy,
  onPromote,
  onDemote,
  onToggleDisabled,
  onSyncRivian,
  onRefreshRivianSession,
  onDelete,
}: {
  row: AdminUserRow;
  currentUserID: string;
  isLastAdmin: boolean;
  busy: boolean;
  onPromote: () => Promise<void>;
  onDemote: () => Promise<void>;
  onToggleDisabled: () => Promise<void>;
  onSyncRivian: () => Promise<void>;
  onRefreshRivianSession: () => Promise<void>;
  onDelete: () => Promise<void>;
}) {
  const isSelf = row.id === currentUserID;
  const detail = useQuery({
    queryKey: ["admin", "user", row.id],
    queryFn: () => backend.adminUserDetail(row.id),
  });
  const d = detail.data;
  const headerName = row.display_name || row.username;

  return (
    <div className="rounded-md border border-neutral-900 bg-neutral-950/40">
      <div className="border-b border-neutral-900 px-4 py-3">
        <div className="flex items-baseline justify-between gap-3">
          <div>
            <div className="text-base font-semibold text-neutral-100">
              {headerName}
            </div>
            <div className="text-xs text-neutral-500">
              {row.email || row.username} · {row.id}
            </div>
          </div>
          <div className="flex items-center gap-2 text-xs">
            <span
              className={`rounded-full border px-2 py-0.5 ${
                row.role === "admin"
                  ? "border-emerald-700 bg-emerald-950 text-emerald-300"
                  : "border-neutral-800 bg-neutral-900 text-neutral-400"
              }`}
            >
              {row.role}
            </span>
            {row.disabled && (
              <span className="rounded-full border border-amber-800 bg-amber-950 px-2 py-0.5 text-amber-300">
                disabled
              </span>
            )}
            {d?.needs_reauth && (
              <span
                className="rounded-full border border-red-900 bg-red-950 px-2 py-0.5 text-red-300"
                title={d.needs_reauth_reason || ""}
              >
                needs re-auth
              </span>
            )}
          </div>
        </div>
      </div>

      <div className="grid gap-4 p-4 sm:grid-cols-2">
        {/* Identity */}
        <Stat label="User ID" value={<code className="text-xs">{row.id}</code>} />
        <Stat label="Created" value={new Date(row.created_at).toLocaleString()} />
        <Stat
          label="Last seen"
          value={
            row.last_seen_at
              ? `${relativeTime(row.last_seen_at)} (${new Date(row.last_seen_at).toLocaleString()})`
              : "never"
          }
        />
        <Stat
          label="Active sessions"
          value={d ? `${d.active_sessions}` : "…"}
        />

        {/* Rivian */}
        <Stat
          label="Rivian"
          value={
            !d
              ? "…"
              : d.rivian_connected
                ? d.rivian_session_at
                  ? `Connected · session ${relativeTime(d.rivian_session_at)}`
                  : "Connected"
                : "Not connected"
          }
          tone={
            d?.needs_reauth
              ? "danger"
              : d?.rivian_connected
                ? "ok"
                : "muted"
          }
        />
        <Stat
          label="Onboarding"
          value={d ? (d.onboarding_completed ? "Completed" : "Pending") : "…"}
          tone={d?.onboarding_completed ? "ok" : "muted"}
        />

        {/* Activity rollups */}
        <Stat
          label="Drives"
          value={
            d
              ? `${d.drive_count} · ${d.drive_miles_total.toFixed(0)} mi`
              : "…"
          }
        />
        <Stat
          label="Charges"
          value={
            d
              ? `${d.charge_count} · ${d.charge_kwh_total.toFixed(0)} kWh`
              : "…"
          }
        />
        <Stat
          label="Samples"
          value={
            d
              ? `${d.sample_count.toLocaleString()}`
              : "…"
          }
        />
        <Stat
          label="Telemetry span"
          value={
            !d
              ? "…"
              : d.oldest_sample_at && d.newest_sample_at
                ? (() => {
                    const label = `${new Date(d.oldest_sample_at).toLocaleDateString()} → ${new Date(d.newest_sample_at).toLocaleDateString()}`;
                    const logsHref = grafanaUserExploreURL(
                      row.id,
                      d.oldest_sample_at,
                      d.newest_sample_at,
                      "logs",
                    );
                    const tracesHref = grafanaUserExploreURL(
                      row.id,
                      d.oldest_sample_at,
                      d.newest_sample_at,
                      "traces",
                    );
                    if (!logsHref && !tracesHref) return label;
                    return (
                      <span>
                        {label}
                        {logsHref && (
                          <>
                            {" · "}
                            <a
                              href={logsHref}
                              target="_blank"
                              rel="noopener"
                              className="text-emerald-300 underline-offset-2 hover:underline"
                              title="Open Loki logs for this user in Grafana"
                            >
                              logs ↗
                            </a>
                          </>
                        )}
                        {tracesHref && (
                          <>
                            {" · "}
                            <a
                              href={tracesHref}
                              target="_blank"
                              rel="noopener"
                              className="text-emerald-300 underline-offset-2 hover:underline"
                              title="Open Tempo traces filtered by user.id"
                            >
                              traces ↗
                            </a>
                          </>
                        )}
                      </span>
                    );
                  })()
                : "no samples"
          }
        />
      </div>

      {/* Vehicles */}
      <div className="border-t border-neutral-900 px-4 py-3">
        <div className="mb-2 text-xs uppercase tracking-wide text-neutral-500">
          Vehicles ({d ? d.vehicles.length : "…"})
        </div>
        {!d ? (
          <div className="text-sm text-neutral-500">Loading…</div>
        ) : !d.vehicles || d.vehicles.length === 0 ? (
          <div className="text-sm text-neutral-500">
            No vehicles on file.
            {row.rivian_connected && (
              <>
                {" "}
                Try <strong>Sync Rivian</strong> below.
              </>
            )}
          </div>
        ) : (
          <ul className="space-y-2">
            {d.vehicles.map((v) => (
              <li
                key={v.id}
                className="rounded-md border border-neutral-900 bg-neutral-950 px-3 py-2"
              >
                <div className="flex items-baseline justify-between gap-2">
                  <div className="text-sm text-neutral-200">
                    {v.display_name || v.model || v.rivian_vehicle_id}
                    {v.model_year ? ` · ${v.model_year}` : null}
                    {v.pack_kwh ? ` · ${v.pack_kwh.toFixed(0)} kWh` : null}
                  </div>
                  <div
                    className="text-xs text-neutral-500"
                    title={v.last_sample_at ?? ""}
                  >
                    {v.last_sample_at
                      ? `last frame ${relativeTime(v.last_sample_at)}`
                      : "no telemetry"}
                  </div>
                </div>
                <div className="mt-1 flex flex-wrap gap-3 text-xs text-neutral-500">
                  {v.vin && <span>VIN {v.vin}</span>}
                  <span>{v.drive_count} drives</span>
                  <span>{v.charge_count} charges</span>
                  <span className="text-neutral-700">
                    rivian id {v.rivian_vehicle_id}
                  </span>
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>

      {/* Signup origin */}
      {d?.signup_request && (
        <div className="border-t border-neutral-900 px-4 py-3">
          <div className="mb-1 text-xs uppercase tracking-wide text-neutral-500">
            Signup origin
          </div>
          <div className="text-sm text-neutral-300">
            Requested {new Date(d.signup_request.requested_at).toLocaleString()}
            {" · "}
            <span
              className={
                d.signup_request.status === "approved"
                  ? "text-emerald-400"
                  : d.signup_request.status === "rejected"
                    ? "text-red-400"
                    : "text-amber-300"
              }
            >
              {d.signup_request.status}
            </span>
            {d.signup_request.decided_at && (
              <span>
                {" "}
                · decided{" "}
                {new Date(d.signup_request.decided_at).toLocaleString()}
              </span>
            )}
          </div>
          {d.signup_request.message && (
            <div className="mt-1 whitespace-pre-wrap rounded-sm bg-neutral-950 p-2 text-xs text-neutral-400">
              {d.signup_request.message}
            </div>
          )}
        </div>
      )}

      {/* Actions */}
      <div className="flex flex-wrap items-center gap-2 border-t border-neutral-900 px-4 py-3">
        {row.role === "user" ? (
          <button
            type="button"
            disabled={busy}
            onClick={onPromote}
            className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-xs text-neutral-300 hover:border-neutral-700 hover:text-neutral-100 disabled:opacity-40"
          >
            Promote to admin
          </button>
        ) : (
          <button
            type="button"
            disabled={busy || isLastAdmin}
            title={isLastAdmin ? "Cannot demote the last admin" : ""}
            onClick={onDemote}
            className="rounded-md border border-neutral-800 bg-neutral-900 px-2 py-1 text-xs text-neutral-300 hover:border-neutral-700 hover:text-neutral-100 disabled:opacity-40"
          >
            Demote to user
          </button>
        )}
        <button
          type="button"
          disabled={
            busy ||
            (!row.disabled && isSelf) ||
            (!row.disabled && row.role === "admin" && isLastAdmin)
          }
          title={
            !row.disabled && isSelf
              ? "Cannot disable your own account"
              : !row.disabled && row.role === "admin" && isLastAdmin
                ? "Cannot disable the last admin"
                : row.disabled
                  ? "Re-enable sign-in for this user"
                  : "Block this user from minting new sessions"
          }
          onClick={onToggleDisabled}
          className={`rounded-md border px-2 py-1 text-xs disabled:opacity-40 ${
            row.disabled
              ? "border-emerald-800 bg-emerald-950/40 text-emerald-300 hover:border-emerald-700 hover:text-emerald-200"
              : "border-amber-900 bg-amber-950/40 text-amber-300 hover:border-amber-800 hover:text-amber-200"
          }`}
        >
          {row.disabled ? "Enable account" : "Disable account"}
        </button>
        <button
          type="button"
          disabled={busy}
          title="Re-fetch this user's vehicles from Rivian"
          onClick={onSyncRivian}
          className="rounded-md border border-cyan-900 bg-cyan-950/40 px-2 py-1 text-xs text-cyan-300 hover:border-cyan-800 hover:text-cyan-200 disabled:opacity-40"
        >
          Sync Rivian
        </button>
        {d?.needs_reauth && (
          <button
            type="button"
            disabled={busy}
            title="Force a fresh CSRF/session token and probe Rivian. Clears the re-auth flag if the session is still valid; if not, the user must sign in again."
            onClick={onRefreshRivianSession}
            className="rounded-md border border-amber-800 bg-amber-950/40 px-2 py-1 text-xs text-amber-200 hover:border-amber-700 hover:text-amber-100 disabled:opacity-40"
          >
            Refresh session
          </button>
        )}
        <div className="ml-auto">
          <button
            type="button"
            disabled={busy || isSelf || isLastAdmin}
            title={
              isSelf
                ? "Cannot delete your own account"
                : isLastAdmin
                  ? "Cannot delete the last admin"
                  : ""
            }
            onClick={onDelete}
            className="rounded-md border border-red-900 bg-red-950/40 px-2 py-1 text-xs text-red-300 hover:border-red-800 hover:text-red-200 disabled:opacity-40"
          >
            Delete user
          </button>
        </div>
      </div>
    </div>
  );
}

// Stat is a tiny label/value row used inside UserDetailPanel. `tone`
// nudges the value colour so the eye can scan for problem cells
// without reading every line.
function Stat({
  label,
  value,
  tone,
}: {
  label: string;
  value: React.ReactNode;
  tone?: "ok" | "danger" | "muted";
}) {
  const valueClass =
    tone === "danger"
      ? "text-red-300"
      : tone === "ok"
        ? "text-emerald-300"
        : tone === "muted"
          ? "text-neutral-500"
          : "text-neutral-200";
  return (
    <div>
      <div className="text-[11px] uppercase tracking-wide text-neutral-500">
        {label}
      </div>
      <div className={`text-sm ${valueClass}`}>{value}</div>
    </div>
  );
}

// SignupRequestsPanel renders the pre-account waitlist. Approve mints
// a single-use invite code, links it on the row, and triggers an
// email to the requester via Resend. The mint result is reflected in
// the row immediately so the admin can copy the code if the email
// failed to send (the response carries email_sent=false in that case).
function SignupRequestsPanel() {
  const qc = useQueryClient();
  const [filter, setFilter] = useState<"pending" | "approved" | "rejected" | "">("pending");
  const [lastApproved, setLastApproved] = useState<{
    email: string;
    link: string;
    sent: boolean;
  } | null>(null);

  const list = useQuery({
    queryKey: ["admin", "signup-requests", filter],
    queryFn: () => backend.adminListSignupRequests(filter),
  });

  const approve = useMutation({
    mutationFn: (id: string) => backend.adminApproveSignupRequest(id),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ["admin", "signup-requests"] });
      if (data.link) {
        setLastApproved({
          email: data.request.email,
          link: data.link,
          sent: data.email_sent,
        });
      }
    },
  });

  const reject = useMutation({
    mutationFn: (id: string) => backend.adminRejectSignupRequest(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["admin", "signup-requests"] }),
  });

  return (
    <div className="space-y-4">
      {/* Filter row */}
      <div className="flex items-center gap-2">
        {(["pending", "approved", "rejected", ""] as const).map((s) => (
          <button
            key={s || "all"}
            type="button"
            onClick={() => setFilter(s)}
            className={`rounded-md px-2 py-1 text-xs ${
              filter === s
                ? "bg-emerald-700 text-white"
                : "bg-neutral-900 text-neutral-400 hover:bg-neutral-800"
            }`}
          >
            {s || "all"}
          </button>
        ))}
      </div>

      {/* Latest-approval banner — shown until the next action so the
          admin can copy the magic link if the email didn't go through. */}
      {lastApproved && (
        <div className="rounded-md border border-emerald-900 bg-emerald-950/40 p-3 text-xs">
          <div className="mb-1 flex items-center justify-between">
            <span className="text-emerald-400">
              Approved {lastApproved.email}
              {lastApproved.sent ? " — email sent" : " — email failed, copy the link below"}
            </span>
            <button
              type="button"
              onClick={() => setLastApproved(null)}
              className="text-[10px] text-neutral-500 hover:text-neutral-300"
            >
              dismiss
            </button>
          </div>
          <div className="flex items-center justify-between gap-2">
            <code className="break-all font-mono text-emerald-300">{lastApproved.link}</code>
            <button
              type="button"
              onClick={() => navigator.clipboard.writeText(lastApproved.link)}
              className="text-[10px] text-neutral-500 hover:text-neutral-300"
            >
              copy
            </button>
          </div>
        </div>
      )}

      {list.isLoading && <p className="text-sm text-neutral-500">Loading…</p>}
      {list.isError && <ErrorBox title="Failed to load signup requests" />}
      {list.data && (
        <SignupRequestsTable
          rows={list.data.requests}
          onApprove={(id) => approve.mutate(id)}
          onReject={(id) => reject.mutate(id)}
          pending={approve.isPending || reject.isPending}
        />
      )}
      {(approve.error || reject.error) && (
        <p className="mt-2 text-xs text-red-400">
          {String(approve.error ?? reject.error)}
        </p>
      )}
    </div>
  );
}

function SignupRequestsTable({
  rows,
  onApprove,
  onReject,
  pending,
}: {
  rows: SignupRequest[];
  onApprove: (id: string) => void;
  onReject: (id: string) => void;
  pending: boolean;
}) {
  if (rows.length === 0) {
    return <p className="text-sm text-neutral-500">No signup requests in this view.</p>;
  }
  return (
    <table className="w-full table-fixed text-xs">
      <thead className="text-left text-neutral-500">
        <tr>
          <th className="w-1/4 py-1.5">Email</th>
          <th className="py-1.5">Message</th>
          <th className="w-24 py-1.5">Requested</th>
          <th className="w-20 py-1.5">Status</th>
          <th className="w-32 py-1.5 text-right">Actions</th>
        </tr>
      </thead>
      <tbody className="divide-y divide-neutral-900 text-neutral-300">
        {rows.map((r) => (
          <tr key={r.id} className="align-top">
            <td className="break-all py-1.5 font-mono">{r.email}</td>
            <td className="whitespace-pre-wrap py-1.5 text-neutral-400">
              {r.message || <span className="text-neutral-600">—</span>}
            </td>
            <td className="py-1.5 text-neutral-500">
              {new Date(r.requested_at).toLocaleDateString()}
            </td>
            <td className="py-1.5">
              <span
                className={
                  r.status === "approved"
                    ? "text-emerald-400"
                    : r.status === "rejected"
                      ? "text-rose-400"
                      : "text-amber-400"
                }
              >
                {r.status}
              </span>
              {r.signup_token && (
                <div className="mt-0.5 text-[10px] text-neutral-500">
                  {r.token_used_at
                    ? "token redeemed"
                    : r.token_expires_at && new Date(r.token_expires_at) < new Date()
                      ? "token expired"
                      : "token pending"}
                </div>
              )}
            </td>
            <td className="py-1.5 text-right">
              {r.status === "pending" ? (
                <div className="flex justify-end gap-1">
                  <button
                    type="button"
                    disabled={pending}
                    onClick={() => onApprove(r.id)}
                    className="rounded-md bg-emerald-700 px-2 py-1 text-[10px] font-medium text-white hover:bg-emerald-600 disabled:opacity-50"
                  >
                    Approve
                  </button>
                  <button
                    type="button"
                    disabled={pending}
                    onClick={() => onReject(r.id)}
                    className="rounded-md bg-neutral-800 px-2 py-1 text-[10px] font-medium text-neutral-300 hover:bg-neutral-700 disabled:opacity-50"
                  >
                    Reject
                  </button>
                </div>
              ) : (
                <span className="text-[10px] text-neutral-600">decided</span>
              )}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
