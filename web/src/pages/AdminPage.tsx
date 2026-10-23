import { useEffect, useMemo, useState } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type AdminUserRow } from "../lib/api";
import { formatDateTime } from "../lib/format";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";
import { useUserActions } from "../components/admin/useUserActions";
import UserPage from "./admin/UserPage";
import { UsersList } from "../components/admin/UsersList";
import AddUser from "../components/admin/AddUser";
import { SignupsNext } from "../components/AdminSignupsNext";
import { FlagsNext } from "../components/AdminFlagsNext";
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
  const tab: AdminTab = onUsersRoute ? "users" : picked ?? "home";
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
  const view: AdminTab = tab;

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap gap-1 border-b border-neutral-800">
        {ADMIN_TABS.map((t) => {
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
            <AddUser />
            <UsersPanel onOpen={(id, from) => navigate(`/admin/users/${id}`, { state: { from } })} />
          </Card>
          <IdpFailuresPanel />
        </div>
      )}
      {view === "signups" && <SignupsNext />}
      {view === "ai" && (
        <Card title="AI providers">
          <AIProvidersPanel />
        </Card>
      )}
      {view === "operations" && <FlagsNext />}
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


// The Users tab: the list, with each user's status and quick actions. A click (or the name link)
// opens /admin/users/:id, the user's own page.
function UsersPanel({ onOpen }: { onOpen: (id: string, from: string) => void }) {
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
      <UsersList
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
