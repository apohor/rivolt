import { useEffect, useState, type FormEvent } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import { ApiError, backend } from "../lib/api";
import { failureMessage, returnMessage } from "../lib/appleLink";
import { APPLE_LINK_COPY as copy } from "../content/appleLinkCopy";
import { Card } from "./ui";

// Sign in with Apple, from Settings → Security: connect an Apple ID to the account you are signed in to, or disconnect it. Both ask for the
// password again (the server decides; an account with no password cannot do either yet). Connecting sends the browser to Apple and Apple back
// to Settings with ?apple=<code>, which this card turns into a line and then takes out of the address. Not shown where linking is off (404) nor
// for the iPhone app or an admin viewing as someone (403). The words are in content/appleLinkCopy.ts.
export function AppleLinkCard({ go = (url: string) => window.location.assign(url) }: { go?: (url: string) => void }) {
  const qc = useQueryClient();
  const status = useQuery({ queryKey: ["auth", "apple-link"], queryFn: () => backend.appleLink(), retry: 0, staleTime: 15_000 });
  const [params, setParams] = useSearchParams();
  const [note, setNote] = useState<{ text: string; bad: boolean } | null>(() => returnMessage(params.get("apple")));
  const [mode, setMode] = useState<"connect" | "disconnect" | null>(null);
  const [password, setPassword] = useState("");

  // The return from Apple is shown once and then taken out of the address, so a reload does not repeat it.
  useEffect(() => {
    if (params.has("apple")) {
      const next = new URLSearchParams(params);
      next.delete("apple");
      setParams(next, { replace: true });
      void qc.invalidateQueries({ queryKey: ["auth", "apple-link"] });
    }
  }, [params, setParams, qc]);

  const connect = useMutation({
    mutationFn: () => backend.appleLinkStart(password),
    onSuccess: (r) => {
      setPassword("");
      setNote({ text: copy.connecting, bad: false });
      go(r.url);
    },
    onError: (e) => {
      setPassword("");
      setNote({ text: failureMessage(e), bad: true });
    },
  });
  const disconnect = useMutation({
    mutationFn: () => backend.appleUnlink(password),
    onSuccess: () => {
      setPassword("");
      setMode(null);
      setNote({ text: copy.disconnected, bad: false });
      void qc.invalidateQueries({ queryKey: ["auth", "apple-link"] });
    },
    onError: (e) => {
      setPassword("");
      setNote({ text: failureMessage(e), bad: true });
    },
  });

  if (status.isError && status.error instanceof ApiError && (status.error.status === 404 || status.error.status === 403)) return null;
  if (status.isPending) return null;
  if (status.isError)
    return (
      <Card title={copy.title} id="apple">
        <p className="text-xs text-rose-400">{copy.loadFailed}</p>
      </Card>
    );
  const { linked, has_password: hasPassword } = status.data;
  const busy = connect.isPending || disconnect.isPending;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    setNote(null);
    if (mode === "connect") connect.mutate();
    else if (mode === "disconnect") disconnect.mutate();
  };

  return (
    <Card title={copy.title} id="apple">
      <div data-apple-link className="space-y-3">
        <p data-apple-state={linked ? "linked" : "not-linked"} className="text-sm font-medium text-neutral-200">
          {linked ? copy.connected : copy.connect}
        </p>
        <p className="max-w-xl text-xs text-neutral-500">{linked ? copy.connectedLine : copy.connectLine}</p>
        {note && (
          <p role="status" data-apple-note className={`max-w-xl text-xs ${note.bad ? "text-rose-400" : "text-neutral-400"}`}>
            {note.text}
          </p>
        )}
        {!hasPassword ? (
          linked ? (
            <div data-apple-needs-password className="space-y-1">
              <button type="button" disabled className="cursor-not-allowed rounded-md border border-neutral-800 px-3 py-1.5 text-xs text-neutral-500">
                {copy.disconnect}
              </button>
              <p className="max-w-xl text-xs text-neutral-500">{copy.cannotDisconnect}</p>
            </div>
          ) : (
            <div data-apple-needs-password className="max-w-xl space-y-1">
              <p className="text-sm font-medium text-neutral-200">{copy.needsPasswordTitle}</p>
              <p className="text-xs text-neutral-500">{copy.needsPasswordBody}</p>
            </div>
          )
        ) : mode ? (
          <form onSubmit={submit} className="space-y-2 rounded-lg border border-neutral-800 p-3">
            <p className="text-sm font-medium text-neutral-200">{mode === "connect" ? copy.connectPromptTitle : copy.disconnectTitle}</p>
            {mode === "disconnect" && <p className="max-w-xl text-xs text-neutral-500">{copy.disconnectBody}</p>}
            <label className="flex flex-col gap-1 text-xs text-neutral-400">
              {copy.passwordLabel}
              <input
                type="password"
                required
                autoFocus
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                className="w-full max-w-xs rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100"
              />
            </label>
            <div className="flex items-center gap-2">
              <button
                type="submit"
                disabled={busy || password === ""}
                className={`rounded-md px-3 py-1.5 text-xs font-semibold text-white disabled:opacity-50 ${mode === "connect" ? "bg-emerald-700 hover:bg-emerald-600" : "bg-rose-700 hover:bg-rose-600"}`}
              >
                {mode === "connect" ? (connect.isPending ? copy.connecting : copy.continueToApple) : disconnect.isPending ? copy.disconnecting : copy.confirmDisconnect}
              </button>
              <button
                type="button"
                disabled={busy}
                onClick={() => {
                  setMode(null);
                  setPassword("");
                }}
                className="rounded-md border border-neutral-700 px-3 py-1.5 text-xs text-neutral-300"
              >
                {mode === "connect" ? copy.cancel : copy.keep}
              </button>
            </div>
          </form>
        ) : (
          <button
            type="button"
            onClick={() => {
              setNote(null);
              setMode(linked ? "disconnect" : "connect");
            }}
            className="rounded-md border border-neutral-700 px-3 py-1.5 text-xs text-neutral-200 hover:border-neutral-500"
          >
            {linked ? copy.disconnect : copy.connect}
          </button>
        )}
      </div>
    </Card>
  );
}
