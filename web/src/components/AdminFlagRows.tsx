import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, type AdminFlagsState, type AdminUserRow, type AudienceFlag } from "../lib/api";
import { audienceBody, audienceConfirm, audienceLabel, audienceOf, capLabel, parseCap, type Audience } from "../lib/adminFlags";
import { ChangedBy } from "./AdminFlagsNext";

// Rows for the Operations flags list: who a feature is on for (Money tab, statement email) and the
// AI call cap. Each change is a short editor, then one confirm, then the save.
function useMergeFlag() {
  const qc = useQueryClient();
  return (patch: Partial<AdminFlagsState>) => {
    qc.setQueryData(["admin", "flags"], (prev: AdminFlagsState | undefined) => (prev ? { ...prev, ...patch } : prev));
    qc.invalidateQueries({ queryKey: ["admin", "audit"] });
  };
}

const userLabel = (u: AdminUserRow) => u.display_name || u.email || u.username;

export function AudienceFlagRow({
  flag, title, feature, description, auditAction, value, note,
}: {
  flag: "statement_email";
  title: string;
  feature: string; // as it reads in "Turn <feature> on for everyone?"
  description: string;
  auditAction: string;
  value: AudienceFlag | undefined;
  note?: string;
}) {
  const merge = useMergeFlag();
  const users = useQuery({ queryKey: ["admin", "users"], queryFn: () => backend.adminListUsers(), retry: 0, staleTime: 60_000 });
  const list = useMemo(() => users.data?.users ?? [], [users.data]);
  const current = audienceOf(value);
  const [editing, setEditing] = useState(false);
  const [choice, setChoice] = useState<Audience>(current);
  const [picked, setPicked] = useState<string[]>(value?.users ?? []);
  const [q, setQ] = useState("");
  const [confirming, setConfirming] = useState(false);
  const save = useMutation({
    mutationFn: async ({ all, users }: { all: boolean; users: string[] }): Promise<AdminFlagsState> =>
      ({ statement_email: (await backend.adminFlagsStatementEmailPut(all, users)).statement_email }) as AdminFlagsState,
    onSuccess: (patch) => {
      merge(patch);
      setEditing(false);
      setConfirming(false);
    },
  });
  const shown = useMemo(() => {
    const n = q.trim().toLowerCase();
    return list.filter((u) => !n || [u.username, u.email, u.display_name].some((v) => v?.toLowerCase().includes(n)));
  }, [list, q]);
  const open = () => { setChoice(current); setPicked(value?.users ?? []); setQ(""); setConfirming(false); setEditing(true); };
  const body = audienceBody(choice, picked);
  const unchanged = choice === current && (choice !== "selected" || [...picked].sort().join() === [...(value?.users ?? [])].sort().join());

  return (
    <li data-flag={flag} className="py-3">
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-2">
        <div className="min-w-[220px] flex-1">
          <div className="flex items-center gap-2 text-neutral-100">
            {title}
            <span data-flag-value className="rounded-full border border-neutral-600 px-2 py-0.5 text-xs text-neutral-200">{audienceLabel(value)}</span>
          </div>
          <div className="mt-0.5 text-xs text-neutral-400">{description}</div>
          {note && <div className="text-xs text-neutral-400">{note}</div>}
          <div className="mt-1"><ChangedBy action={auditAction} /></div>
        </div>
        {!editing && (
          <button type="button" onClick={open} className="rounded-md border border-neutral-700 px-2.5 py-1 text-xs text-neutral-200 hover:border-neutral-500">
            Change…
          </button>
        )}
      </div>

      {editing && (
        <div data-flag-editor className="mt-3 space-y-3 rounded-md border border-neutral-800 p-3">
          <fieldset>
            <legend className="text-xs font-medium text-neutral-400">On for</legend>
            <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1 text-sm text-neutral-200">
              {([["nobody", "Nobody"], ["selected", "Selected users"], ["everyone", "Everyone"]] as const).map(([id, label]) => (
                <label key={id} className="flex items-center gap-1.5">
                  <input type="radio" name={`${flag}-audience`} checked={choice === id} onChange={() => { setChoice(id); setConfirming(false); }} />
                  {label}
                </label>
              ))}
            </div>
          </fieldset>
          {choice === "selected" && (
            <div className="space-y-2">
              <input type="search" aria-label={`Search users for ${title}`} placeholder="Search name or email" value={q} onChange={(e) => setQ(e.target.value)} className="input !w-full sm:!w-64" />
              <ul className="max-h-48 divide-y divide-neutral-800 overflow-y-auto rounded-md border border-neutral-800 text-sm">
                {shown.map((u) => (
                  <li key={u.id}>
                    <label className="flex items-center gap-2 px-3 py-1.5">
                      <input
                        type="checkbox"
                        checked={picked.includes(u.id)}
                        onChange={(e) => { setPicked((p) => (e.target.checked ? [...p, u.id] : p.filter((x) => x !== u.id))); setConfirming(false); }}
                      />
                      <span className="text-neutral-100">{userLabel(u)}</span>
                      <span className="text-xs text-neutral-400">{u.email && u.email !== userLabel(u) ? u.email : ""}</span>
                    </label>
                  </li>
                ))}
                {shown.length === 0 && <li className="px-3 py-2 text-xs text-neutral-400">No users match.</li>}
              </ul>
              <p className="text-xs text-neutral-400">{picked.length} selected</p>
            </div>
          )}

          {confirming ? (
            <div role="alertdialog" aria-label="Confirm" className="rounded-md border border-rose-500/40 bg-rose-500/10 p-3 text-sm">
              <p className="font-medium text-rose-200">{audienceConfirm(feature, current, choice, picked.length)}</p>
              <div className="mt-2 flex gap-2">
                <button type="button" disabled={save.isPending} onClick={() => save.mutate(body)} className="rounded-md bg-rose-700 px-3 py-1.5 text-sm font-semibold text-white hover:bg-rose-600 disabled:opacity-50">
                  {save.isPending ? "Saving…" : "Confirm"}
                </button>
                <button type="button" onClick={() => setConfirming(false)} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200">Back</button>
              </div>
            </div>
          ) : (
            <div className="flex gap-2">
              <button type="button" disabled={unchanged || (choice === "selected" && picked.length === 0)} onClick={() => setConfirming(true)} className="rounded-md border border-emerald-700 bg-emerald-900/40 px-3 py-1.5 text-sm text-emerald-100 hover:bg-emerald-900/60 disabled:opacity-50">
                Save…
              </button>
              <button type="button" onClick={() => setEditing(false)} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200">Cancel</button>
            </div>
          )}
          {save.isError && <p role="alert" className="text-xs text-rose-300">Save failed: {String((save.error as Error).message)}</p>}
        </div>
      )}
    </li>
  );
}

export function AICallCapRow({ value }: { value: AdminFlagsState["ai_call_cap"] }) {
  const merge = useMergeFlag();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [confirming, setConfirming] = useState(false);
  const parsed = parseCap(draft);
  const save = useMutation({
    mutationFn: (n: number) => backend.adminFlagsAICallCapPut(n),
    onSuccess: (data) => { merge({ ai_call_cap: data.ai_call_cap }); setEditing(false); setConfirming(false); },
  });
  const current = value?.daily_limit ?? 0;
  return (
    <li data-flag="ai_call_cap" className="py-3">
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-2">
        <div className="min-w-[220px] flex-1">
          <div className="flex items-center gap-2 text-neutral-100">
            AI call cap
            <span data-flag-value className="rounded-full border border-neutral-600 px-2 py-0.5 text-xs text-neutral-200">{capLabel(current)}</span>
          </div>
          <div className="mt-0.5 text-xs text-neutral-400">How many AI calls each user can make per day. 0 removes the cap.</div>
          <div className="mt-1"><ChangedBy action="flag.ai_call_cap" /></div>
        </div>
        {!editing && (
          <button type="button" onClick={() => { setDraft(String(current)); setConfirming(false); setEditing(true); }} className="rounded-md border border-neutral-700 px-2.5 py-1 text-xs text-neutral-200 hover:border-neutral-500">
            Change…
          </button>
        )}
      </div>
      {editing && (
        <div data-flag-editor className="mt-3 space-y-3 rounded-md border border-neutral-800 p-3">
          <label className="block text-xs font-medium text-neutral-400">
            Calls per user per day
            <input type="number" min={0} inputMode="numeric" value={draft} onChange={(e) => { setDraft(e.target.value); setConfirming(false); }} className="input mt-1 !w-32" />
          </label>
          {draft.trim() !== "" && parsed === null && <p role="alert" className="text-xs text-rose-300">Enter a whole number, 0 or more.</p>}
          {confirming && parsed !== null ? (
            <div role="alertdialog" aria-label="Confirm" className="rounded-md border border-rose-500/40 bg-rose-500/10 p-3 text-sm">
              <p className="font-medium text-rose-200">{parsed === 0 ? "Remove the AI call cap?" : `Cap AI calls at ${parsed.toLocaleString()} per user per day?`}</p>
              <div className="mt-2 flex gap-2">
                <button type="button" disabled={save.isPending} onClick={() => save.mutate(parsed)} className="rounded-md bg-rose-700 px-3 py-1.5 text-sm font-semibold text-white hover:bg-rose-600 disabled:opacity-50">
                  {save.isPending ? "Saving…" : "Confirm"}
                </button>
                <button type="button" onClick={() => setConfirming(false)} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200">Back</button>
              </div>
            </div>
          ) : (
            <div className="flex gap-2">
              <button type="button" disabled={parsed === null || parsed === current} onClick={() => setConfirming(true)} className="rounded-md border border-emerald-700 bg-emerald-900/40 px-3 py-1.5 text-sm text-emerald-100 hover:bg-emerald-900/60 disabled:opacity-50">Save…</button>
              <button type="button" onClick={() => setEditing(false)} className="rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200">Cancel</button>
            </div>
          )}
          {save.isError && <p role="alert" className="text-xs text-rose-300">Save failed: {String((save.error as Error).message)}</p>}
        </div>
      )}
    </li>
  );
}
