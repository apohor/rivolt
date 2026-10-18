import { useEffect, useMemo, useState } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type AdminFlagsState, type AdminUserRow, type SignupRequest } from "../lib/api";
import { formatDateTime } from "../lib/format";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { useUserActions } from "../components/admin/useUserActions";
import UserPage from "./admin/UserPage";
import { AdminUsersTable } from "../components/AdminUsersTable";
import { SignupsNext } from "../components/AdminSignupsNext";
import { FlagsNext } from "../components/AdminFlagsNext";
import { useUiNext } from "../lib/uiNext";
import { AuditNext } from "../components/AdminAuditNext";
import { HomeNext } from "../components/AdminHomeNext";
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

// AdminTabs groups admin-only surfaces under a single tab nav. The
// active tab is mirrored in the URL hash so a reload (or a copy-
// pasted link to /admin#ai) keeps the position the operator was on.
type AdminTab = "home" | "users" | "signups" | "ai" | "operations" | "tuning" | "audit";

// Signups is first because pending waitlist requests are the
// most time-sensitive thing on this page — every other tab is
// reactive ops (a user reported X) or background tuning.
const ADMIN_TABS: { id: AdminTab; label: string }[] = [
  { id: "home", label: "Home" },
  { id: "signups", label: "Signups" },
  { id: "users", label: "Users" },
  { id: "ai", label: "AI" },
  { id: "operations", label: "Operations" },
  { id: "tuning", label: "Tuning" },
  { id: "audit", label: "Audit" },
];

function isAdminTab(s: string): s is AdminTab {
  return ADMIN_TABS.some((t) => t.id === s);
}

function AdminTabs({ currentUserID }: { currentUserID: string }) {
  const nextUi = useUiNext();
  const navigate = useNavigate();
  const { pathname } = useLocation();
  // /admin/users and /admin/users/:id are real routes (deep links, a working back button); every other
  // tab lives at /admin with its name in the hash.
  const onUsersRoute = pathname.startsWith("/admin/users");
  const userID = useParams().id;
  const initial: AdminTab | null = (() => {
    const h = window.location.hash.replace(/^#/, "");
    return isAdminTab(h) ? h : null;
  })();
  // Until a tab is chosen (or deep-linked) the first one shows: Home in the new look, Signups before.
  const [picked, setTab] = useState<AdminTab | null>(initial);
  const tab: AdminTab = onUsersRoute ? "users" : picked ?? (nextUi ? "home" : "signups");
  // Keep the hash in sync so a reload restores the active tab and
  // a deep link (`/admin#ai`) opens the right pane. Listen for
  // hashchange too — the back button moves between tabs cleanly.
  useEffect(() => {
    if (!onUsersRoute && picked && window.location.hash.replace(/^#/, "") !== picked) {
      window.history.replaceState(null, "", `#${picked}`);
    }
  }, [picked, onUsersRoute]);
  useEffect(() => {
    const onHash = () => {
      const h = window.location.hash.replace(/^#/, "");
      if (isAdminTab(h)) setTab(h);
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);

  // Home and Audit exist only in the new look (the flag arrives after first render, so a #home or
  // #audit deep link keeps its hash and shows once it is on; with it off the first tab shows instead).
  const view: AdminTab = (tab === "audit" || tab === "home") && !nextUi ? "signups" : tab;

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-1 border-b border-neutral-800">
        {ADMIN_TABS.filter((t) => (t.id !== "audit" && t.id !== "home") || nextUi).map((t) => {
          const active = t.id === tab;
          return (
            <button
              key={t.id}
              type="button"
              onClick={() => {
                if (t.id === "users") navigate("/admin/users");
                else if (onUsersRoute) navigate(`/admin#${t.id}`);
                else setTab(t.id);
              }}
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
      {view === "home" && <HomeNext goTo={(t) => (t === "users" ? navigate("/admin/users") : setTab(t))} />}
      {view === "users" && userID && <UserPage currentUserID={currentUserID} />}
      {view === "users" && !userID && (
        <div className="space-y-4">
          <Card title="Users">
            <CreateUserForm />
            <UsersPanel onOpen={(id) => navigate(`/admin/users/${id}`)} />
          </Card>
          <IdpFailuresPanel />
        </div>
      )}
      {view === "signups" && (nextUi ? <SignupsNext /> : (
        <Card title="Signup requests">
          <SignupRequestsPanel />
        </Card>
      ))}
      {view === "ai" && (
        <Card title="AI providers">
          <AIProvidersPanel />
        </Card>
      )}
      {view === "operations" && nextUi && <FlagsNext />}
      {view === "operations" && !nextUi && (
        <div className="space-y-4">
          <Card title="Feature flags">
            <FeatureFlagsPanel />
          </Card>
          <Card title="Signup cap">
            <SignupCapPanel />
          </Card>
        </div>
      )}
      {view === "audit" && <AuditNext />}
      {view === "tuning" && (
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
        aria-label="Role"
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

// The Users tab: the list, with each user's status and quick actions. A click (or the name link)
// opens /admin/users/:id, the user's own page.
function UsersPanel({ onOpen }: { onOpen: (id: string) => void }) {
  const q = useQuery({
    queryKey: ["admin", "users"],
    queryFn: () => backend.adminListUsers(),
  });
  const acts = useUserActions();
  const rows: AdminUserRow[] = useMemo(() => q.data?.users ?? [], [q.data]);
  if (q.isLoading) return <Spinner />;
  if (q.isError) return <ErrorBox title="Failed to load users" detail={String(q.error)} />;
  // The server refuses to demote, disable or delete the last admin; the list mirrors it by disabling the buttons.
  const adminCount = rows.filter((u) => u.role === "admin" && !u.disabled).length;
  return (
    <div className="space-y-3">
      <AdminUsersTable
        rows={rows}
        onSelect={onOpen}
        busyID={acts.busyID}
        adminCount={adminCount}
        actions={{
          sync: (u) => void acts.sync(u),
          refresh: (u) => void acts.refreshSession(u),
          toggleDisabled: (u) => void acts.toggleDisabled(u),
          remove: (u) => void acts.remove(u),
        }}
      />
      {acts.note && <p role="status" className="text-xs text-neutral-300">{acts.note}</p>}
    </div>
  );
}

// SignupRequestsPanel renders the pre-account waitlist. Approve mints
// a single-use invite code, links it on the row, and triggers an
// email to the requester via Resend. The mint result is reflected in
// the row immediately so the admin can copy the code if the email
// failed to send (the response carries email_sent=false in that case).
// IdpFailuresPanel lists identity-provider deletes that failed after
// the Rivolt account was removed, with a Retry per row. Rows whose
// account exists again get no Retry: delete that account instead.
export function IdpFailuresPanel() {
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["admin", "idp-failures"],
    queryFn: () => backend.adminIdpFailures(),
    retry: 1,
  });
  const retry = useMutation({
    mutationFn: (id: string) => backend.adminRetryIdpFailure(id),
    onSettled: () => qc.invalidateQueries({ queryKey: ["admin", "idp-failures"] }),
  });
  const rows = list.data ?? [];
  if (list.isLoading) return null;
  if (list.isError) {
    return (
      <Card title="Sign-in removal failures">
        <p className="text-xs text-neutral-500">Couldn&apos;t load the failure list.</p>
      </Card>
    );
  }
  return (
    <Card title="Sign-in removal failures">
      {rows.length === 0 ? (
        <p className="text-xs text-neutral-500">None. Every deleted account&apos;s sign-in was removed.</p>
      ) : (
        <ul className="divide-y divide-neutral-800">
          {rows.map((r) => {
            const busy = retry.isPending && retry.variables === r.id;
            const failedAgain = retry.isError && retry.variables === r.id;
            return (
              <li key={r.id} className="flex flex-wrap items-start justify-between gap-2 py-2 text-sm">
                <div className="min-w-0">
                  <div className="text-neutral-100">{r.username}</div>
                  <div className="text-xs text-neutral-500">
                    {r.attempts} {r.attempts === 1 ? "attempt" : "attempts"} · last tried{" "}
                    {formatDateTime(r.last_tried)}
                  </div>
                  <div className="break-words text-xs text-rose-300">{r.error}</div>
                  {r.account_exists && (
                    <div className="mt-1 text-xs text-amber-300">
                      This person signed in again and has a Rivolt account. Delete that account instead of retrying.
                    </div>
                  )}
                  {failedAgain && (
                    <div role="status" className="text-xs text-amber-300">
                      Still failing: {String(retry.error)}
                    </div>
                  )}
                </div>
                {!r.account_exists && (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => retry.mutate(r.id)}
                    className="rounded-md border border-neutral-700 px-2 py-1 text-xs text-neutral-200 hover:bg-neutral-800 disabled:opacity-50"
                  >
                    {busy ? "Retrying…" : "Retry"}
                  </button>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </Card>
  );
}

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
                    className="rounded-md bg-emerald-700 px-2 py-1 text-[10px] font-medium text-white hover:bg-emerald-800 disabled:opacity-50"
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
