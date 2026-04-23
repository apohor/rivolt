import { useEffect, useState } from "react";
import {
  disablePush,
  enablePush,
  readPushState,
  sendTestPush,
  type PushState,
} from "../lib/push";
import { Card, ErrorBox, Spinner, Toggle } from "./ui";

// NotificationsSection renders the push-notification controls for the
// Settings page.
//
// Push is inherently per-device: a subscription lives in a single browser
// on a single physical device. So we don't try to pretend this is account-
// level config — the UI just reflects "is *this* device subscribed?" and
// lets the user flip it.
export default function NotificationsSection() {
  const [state, setState] = useState<PushState | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [testStatus, setTestStatus] = useState<string | null>(null);

  useEffect(() => {
    readPushState().then(setState).catch((e) => setErr(String(e)));
  }, []);

  const supported = state?.supported ?? false;
  const serverOk = state?.server?.enabled ?? false;
  const subscribed = Boolean(state?.endpoint);
  const count = state?.server?.subscription_count ?? 0;

  async function toggle() {
    setErr(null);
    setBusy(true);
    try {
      const next = subscribed ? await disablePush() : await enablePush();
      setState(next);
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function test() {
    setErr(null);
    setTestStatus("sending…");
    try {
      await sendTestPush();
      setTestStatus("sent — if nothing arrives in a few seconds, check OS/browser permissions");
    } catch (e) {
      setTestStatus(null);
      setErr(e instanceof Error ? e.message : String(e));
    }
  }

  return (
    <Card title="Notifications">
      {state === null && <Spinner />}
      {err && <ErrorBox title="Push error" detail={err} />}

      {state !== null && (
        <div className="space-y-4">
          <div className="text-xs text-neutral-400 leading-relaxed">
            Get a push notification the moment a shot finishes or an AI
            analysis is ready. Works on desktop browsers, Android, and on
            iPhone/iPad only when Caffeine is installed to the home screen
            (Safari &rarr; Share &rarr; Add to Home Screen).
          </div>

          <StatusLine
            label="Browser support"
            ok={supported}
            detail={
              supported
                ? "service worker + PushManager available"
                : "not supported on this device (install to home screen on iOS)"
            }
          />
          <StatusLine
            label="Server"
            ok={serverOk}
            detail={
              serverOk
                ? `ready · ${count} subscription${count === 1 ? "" : "s"} stored`
                : "push disabled on the server"
            }
          />
          <StatusLine
            label="This device"
            ok={subscribed}
            detail={subscribed ? "subscribed" : "not subscribed"}
          />

          <div className="flex items-center gap-3 pt-2">
            <Toggle
              checked={subscribed}
              disabled={!supported || !serverOk || busy}
              onChange={() => void toggle()}
            />
            <span className="text-sm text-neutral-300">
              {busy
                ? "working…"
                : subscribed
                  ? "Notifications on for this device"
                  : "Enable notifications for this device"}
            </span>
          </div>

          {subscribed && (
            <div className="flex items-center gap-3 pt-1">
              <button
                type="button"
                onClick={() => void test()}
                className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-xs hover:bg-neutral-800 disabled:opacity-40"
                disabled={busy}
              >
                Send test notification
              </button>
              {testStatus && (
                <span className="text-xs text-neutral-500">{testStatus}</span>
              )}
            </div>
          )}

          {state.permission === "denied" && (
            <div className="rounded-md border border-amber-700/50 bg-amber-900/10 px-3 py-2 text-xs text-amber-200">
              Notifications are blocked for this site. Open your browser's
              site settings to re-enable them.
            </div>
          )}
        </div>
      )}
    </Card>
  );
}

function StatusLine({
  label,
  ok,
  detail,
}: {
  label: string;
  ok: boolean;
  detail: string;
}) {
  return (
    <div className="flex items-start gap-3 text-sm">
      <span
        aria-hidden
        className={`mt-1 inline-block h-2 w-2 shrink-0 rounded-full ${
          ok ? "bg-emerald-500" : "bg-neutral-600"
        }`}
      />
      <div className="min-w-0 flex-1">
        <div className="text-neutral-200">{label}</div>
        <div className="text-xs text-neutral-500">{detail}</div>
      </div>
    </div>
  );
}
