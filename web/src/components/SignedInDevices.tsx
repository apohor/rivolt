import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, backend, type AuthSession } from "../lib/api";
import { formatDateTime } from "../lib/format";
import { activeAgo, deviceName } from "../lib/deviceLabel";
import { Card } from "./ui";

// The browsers and the iPhone app signed in as this user, so a lost phone or a borrowed computer can be signed out. A device is
// named as the server stored it (a device type, a browser and system), with when it signed in and was last used; no address
// and no place. Revoking the one in hand signs this browser out.
export function SignedInDevices() {
  const qc = useQueryClient();
  const list = useQuery({
    queryKey: ["auth", "sessions"],
    queryFn: () => backend.authSessions(),
    retry: 0,
    staleTime: 15_000,
  });
  const [confirming, setConfirming] = useState<string | "others" | null>(null);
  const [note, setNote] = useState<string | null>(null);

  const refresh = () => qc.invalidateQueries({ queryKey: ["auth", "sessions"] });
  const revoke = useMutation({
    mutationFn: async (s: AuthSession) => {
      await backend.revokeAuthSession(s.id);
      return s;
    },
    onSuccess: (s) => {
      setConfirming(null);
      if (s.current) {
        window.location.assign("/login"); // this browser's own sign-in is gone
        return;
      }
      setNote("Signed out.");
      void refresh();
    },
    onError: (e) => setNote(e instanceof Error ? e.message : "Couldn't sign that device out."),
  });
  const revokeOthers = useMutation({
    mutationFn: () => backend.revokeOtherAuthSessions(),
    onSuccess: (r) => {
      setConfirming(null);
      setNote(r.revoked === 1 ? "Signed out 1 other device." : `Signed out ${r.revoked} other devices.`);
      void refresh();
    },
    onError: (e) =>
      setNote(
        e instanceof ApiError && e.status === 409
          ? "This sign-in has no session to keep. Sign in with a browser or the app first."
          : e instanceof Error
            ? e.message
            : "Couldn't sign the other devices out.",
      ),
  });

  // Not on this install (404), or an admin viewing as someone (403): nothing to show.
  if (list.isError && list.error instanceof ApiError && (list.error.status === 404 || list.error.status === 403)) return null;
  if (list.isPending)
    return (
      <Card title="Signed-in devices" id="devices">
        <p className="text-xs text-neutral-500">Loading…</p>
      </Card>
    );
  if (list.isError)
    return (
      <Card title="Signed-in devices" id="devices">
        <p className="text-xs text-rose-400">Couldn't load your signed-in devices.</p>
      </Card>
    );

  const rows = list.data.sessions;
  const others = rows.filter((s) => !s.current).length;
  const hasCurrent = rows.some((s) => s.current);
  const busy = revoke.isPending || revokeOthers.isPending;

  return (
    <Card title="Signed-in devices" id="devices">
      <div data-signed-in-devices className="space-y-3">
        <p className="text-xs text-neutral-500 max-w-xl">
          Where you're signed in to Rivolt: this browser, other browsers, and the iPhone app. Sign out any you don't recognize. A device is
          named by its type or browser; no address or place is kept for it here.
        </p>
        {rows.length === 0 ? (
          <p className="text-sm text-neutral-400">No signed-in devices.</p>
        ) : (
          <ul className="divide-y divide-neutral-800 rounded-lg border border-neutral-800">
            {rows.map((s) => (
              <li key={s.id} data-device-row className="flex flex-wrap items-center justify-between gap-3 px-3 py-2.5">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-sm text-neutral-100">
                    <span>{deviceName(s)}</span>
                    <span className="rounded bg-neutral-800 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-neutral-400">
                      {s.kind === "app" ? "App" : "Browser"}
                    </span>
                    {s.current && (
                      <span
                        data-device-current
                        className="rounded bg-emerald-900/50 px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-emerald-300"
                      >
                        This device
                      </span>
                    )}
                  </div>
                  <div className="text-xs text-neutral-500">
                    Signed in {formatDateTime(s.created_at)} · active {activeAgo(s.last_seen_at)}
                  </div>
                </div>
                {confirming === s.id ? (
                  <span className="flex items-center gap-2 text-xs">
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => revoke.mutate(s)}
                      className="rounded-md bg-rose-700 px-2.5 py-1 font-semibold text-white hover:bg-rose-600 disabled:opacity-50"
                    >
                      {s.current ? "Sign out this device" : "Yes, sign out"}
                    </button>
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => setConfirming(null)}
                      className="rounded-md border border-neutral-700 px-2.5 py-1 text-neutral-300 hover:border-neutral-500"
                    >
                      Cancel
                    </button>
                  </span>
                ) : (
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => {
                      setNote(null);
                      setConfirming(s.id);
                    }}
                    aria-label={`Sign out ${deviceName(s)}`}
                    className="rounded-md border border-neutral-700 px-2.5 py-1 text-xs text-neutral-300 hover:border-neutral-500 disabled:opacity-50"
                  >
                    Sign out
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
        {others > 0 &&
          (confirming === "others" ? (
            <span className="flex items-center gap-2 text-xs">
              <button
                type="button"
                disabled={busy}
                onClick={() => revokeOthers.mutate()}
                className="rounded-md bg-rose-700 px-2.5 py-1 font-semibold text-white hover:bg-rose-600 disabled:opacity-50"
              >
                Yes, sign out {others} other {others === 1 ? "device" : "devices"}
              </button>
              <button
                type="button"
                disabled={busy}
                onClick={() => setConfirming(null)}
                className="rounded-md border border-neutral-700 px-2.5 py-1 text-neutral-300 hover:border-neutral-500"
              >
                Cancel
              </button>
            </span>
          ) : (
            <button
              type="button"
              disabled={busy}
              onClick={() => {
                setNote(hasCurrent ? null : "This sign-in has no session to keep. Sign in with a browser or the app first.");
                if (hasCurrent) setConfirming("others");
              }}
              className="rounded-md border border-neutral-700 px-2.5 py-1 text-xs text-neutral-300 hover:border-neutral-500 disabled:opacity-50"
            >
              Sign out everywhere else
            </button>
          ))}
        {note && (
          <p role="status" data-devices-note className="text-xs text-neutral-400">
            {note}
          </p>
        )}
      </div>
    </Card>
  );
}
