// BeansPage: small CRUD for the user's coffee beans inventory.
//
// Each shot can be linked to one bean via the feedback card, so the app
// can answer "how did my Honduran natural pull this week" and the AI
// coach can factor origin/roast age into suggestions.
import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, MachineError, type Bean, type BeanInput } from "../lib/api";
import { Card, ErrorBox, PageHeader, Spinner } from "../components/ui";

const empty: BeanInput = {
  name: "",
  roaster: "",
  origin: "",
  process: "",
  roast_level: "",
  roast_date: "",
  notes: "",
  default_grind_size: "",
  default_grind_rpm: null,
};

export default function BeansPage() {
  const qc = useQueryClient();
  const [editing, setEditing] = useState<Bean | null>(null);
  const [draft, setDraft] = useState<BeanInput>(empty);
  const [open, setOpen] = useState(false);

  const list = useQuery({ queryKey: ["beans"], queryFn: () => backend.listBeans() });

  const save = useMutation({
    mutationFn: async () => {
      if (editing) return backend.updateBean(editing.id, draft);
      return backend.createBean(draft);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["beans"] });
      setOpen(false);
      setEditing(null);
      setDraft(empty);
      setScanConfidence(null);
    },
  });

  const del = useMutation({
    mutationFn: (id: string) => backend.deleteBean(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["beans"] }),
  });

  // Activating a bag also invalidates the shots list because new
  // shots will be auto-tagged with this bean id going forward.
  const activate = useMutation({
    mutationFn: (id: string | null) =>
      id ? backend.setBeanActive(id) : backend.clearBeanActive(),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["beans"] });
      qc.invalidateQueries({ queryKey: ["shots"] });
    },
  });

  // Scan a bag photo: pre-fills the new-bag form from what the LLM
  // read off the label, then the user reviews/edits before saving.
  // Image never leaves memory on the server — we don't persist it.
  const fileInputRef = useRef<HTMLInputElement | null>(null);
  const [scanConfidence, setScanConfidence] = useState<number | null>(null);
  const scan = useMutation({
    mutationFn: (file: File) => backend.extractBeanFromImage(file),
    onSuccess: (res) => {
      setEditing(null);
      setDraft({
        name: res.name ?? "",
        roaster: res.roaster ?? "",
        origin: res.origin ?? "",
        process: res.process ?? "",
        roast_level: res.roast_level ?? "",
        roast_date: res.roast_date ?? "",
        notes: res.notes ?? "",
        default_grind_size: "",
        default_grind_rpm: null,
      });
      setScanConfidence(typeof res.confidence === "number" ? res.confidence : null);
      setOpen(true);
    },
  });

  function openNew() {
    setEditing(null);
    setDraft(empty);
    setOpen(true);
  }

  function openEdit(b: Bean) {
    setEditing(b);
    setDraft({
      name: b.name,
      roaster: b.roaster ?? "",
      origin: b.origin ?? "",
      process: b.process ?? "",
      roast_level: b.roast_level ?? "",
      roast_date: b.roast_date ?? "",
      notes: b.notes ?? "",
      archived: b.archived,
      default_grind_size: b.default_grind_size ?? "",
      default_grind_rpm: b.default_grind_rpm ?? null,
    });
    setOpen(true);
  }

  const beans = list.data ?? [];
  const active = beans.filter((b) => !b.archived);
  const archived = beans.filter((b) => b.archived);
  const activeBag = beans.find((b) => b.active);

  return (
    <div className="space-y-6">
      <PageHeader
        title="Beans"
        subtitle={
          activeBag
            ? `Active bag: ${activeBag.name}${activeBag.roaster ? ` · ${activeBag.roaster}` : ""} — new shots auto-tagged`
            : beans.length > 0
            ? `${active.length} active · ${archived.length} archived — mark one "in use" to auto-tag new shots`
            : "Track the bags you're pulling with"
        }
        actions={
          <div className="flex gap-2">
            <input
              ref={fileInputRef}
              type="file"
              accept="image/*"
              // capture="environment" nudges mobile Safari/Chrome to open
              // the rear camera directly instead of the full photo roll.
              // Desktop ignores it and shows the normal file picker.
              capture="environment"
              className="hidden"
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f) scan.mutate(f);
                // reset so picking the same file twice re-triggers change
                e.target.value = "";
              }}
            />
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              disabled={scan.isPending}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm hover:bg-neutral-800 disabled:opacity-40"
              title="Take or upload a photo of a coffee bag; we'll pre-fill the form"
            >
              {scan.isPending ? "Scanning…" : "Scan bag"}
            </button>
            <button
              type="button"
              onClick={openNew}
              className="rounded-md border border-emerald-800 bg-emerald-900/40 px-3 py-1.5 text-sm text-emerald-200 hover:bg-emerald-900/60"
            >
              Add bag
            </button>
          </div>
        }
      />

      {scan.error && (
        <ErrorBox
          title="Scan failed"
          detail={
            scan.error instanceof MachineError
              ? `${scan.error.status}: ${JSON.stringify(scan.error.body)}`
              : String(scan.error)
          }
        />
      )}
      {open && scanConfidence !== null && (
        <div
          className={`rounded-md border px-3 py-2 text-xs ${
            scanConfidence >= 0.6
              ? "border-emerald-800 bg-emerald-950/30 text-emerald-200"
              : "border-amber-800 bg-amber-950/30 text-amber-200"
          }`}
        >
          Scanned bag with {Math.round(scanConfidence * 100)}% confidence — review
          the fields below before saving.
        </div>
      )}

      {open && (
        <Card title={editing ? `Edit "${editing.name}"` : "New bag"}>
          <div className="grid gap-3 md:grid-cols-2">
            <Field label="Name *">
              <input
                className="input"
                value={draft.name}
                onChange={(e) => setDraft((d) => ({ ...d, name: e.target.value }))}
                placeholder="e.g. Onyx Monarch"
                autoFocus
              />
            </Field>
            <Field label="Roaster">
              <input
                className="input"
                value={draft.roaster ?? ""}
                onChange={(e) => setDraft((d) => ({ ...d, roaster: e.target.value }))}
              />
            </Field>
            <Field label="Origin">
              <input
                className="input"
                value={draft.origin ?? ""}
                onChange={(e) => setDraft((d) => ({ ...d, origin: e.target.value }))}
                placeholder="Country / farm / blend"
              />
            </Field>
            <Field label="Process">
              <input
                className="input"
                value={draft.process ?? ""}
                onChange={(e) => setDraft((d) => ({ ...d, process: e.target.value }))}
                placeholder="washed / natural / honey"
              />
            </Field>
            <Field label="Roast level">
              <input
                className="input"
                value={draft.roast_level ?? ""}
                onChange={(e) => setDraft((d) => ({ ...d, roast_level: e.target.value }))}
                placeholder="light / medium / dark"
              />
            </Field>
            <Field label="Roast date">
              <input
                type="date"
                className="input"
                value={draft.roast_date ?? ""}
                onChange={(e) => setDraft((d) => ({ ...d, roast_date: e.target.value }))}
              />
            </Field>
            <Field label="Default grind size">
              <input
                className="input"
                value={draft.default_grind_size ?? ""}
                onChange={(e) =>
                  setDraft((d) => ({ ...d, default_grind_size: e.target.value }))
                }
                placeholder="e.g. 2.8 or 12 clicks"
              />
            </Field>
            <Field label="Default grind RPM">
              <input
                type="number"
                inputMode="numeric"
                min={0}
                step={10}
                className="input"
                value={draft.default_grind_rpm ?? ""}
                onChange={(e) => {
                  const v = e.target.value;
                  setDraft((d) => ({
                    ...d,
                    default_grind_rpm: v === "" ? null : Number(v),
                  }));
                }}
                placeholder="800 (variable-speed grinders only)"
              />
            </Field>
            <div className="md:col-span-2">
              <Field label="Notes">
                <textarea
                  rows={3}
                  className="input"
                  value={draft.notes ?? ""}
                  onChange={(e) => setDraft((d) => ({ ...d, notes: e.target.value }))}
                  placeholder="Tasting notes, bag weight, anything useful"
                />
              </Field>
            </div>
            {editing && (
              <label className="flex items-center gap-2 text-xs text-neutral-300 md:col-span-2">
                <input
                  type="checkbox"
                  checked={!!draft.archived}
                  onChange={(e) => setDraft((d) => ({ ...d, archived: e.target.checked }))}
                />
                Archived (hide from pickers)
              </label>
            )}
          </div>
          {save.error && (
            <ErrorBox
              title="Save failed"
              detail={
                save.error instanceof MachineError
                  ? `${save.error.status}: ${JSON.stringify(save.error.body)}`
                  : String(save.error)
              }
            />
          )}
          <div className="mt-4 flex gap-2">
            <button
              type="button"
              disabled={save.isPending || !draft.name.trim()}
              onClick={() => save.mutate()}
              className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
            >
              {save.isPending ? "Saving…" : editing ? "Save changes" : "Create"}
            </button>
            <button
              type="button"
              onClick={() => {
                setOpen(false);
                setEditing(null);
                setDraft(empty);
                setScanConfidence(null);
              }}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm hover:bg-neutral-800"
            >
              Cancel
            </button>
          </div>
        </Card>
      )}

      {list.isLoading ? (
        <Spinner />
      ) : list.error ? (
        <ErrorBox title="Couldn't load beans" detail={String(list.error)} />
      ) : beans.length === 0 ? (
        <Card title="No beans yet">
          <p className="text-sm text-neutral-400">
            Add your first bag — the name is all that's required. Everything
            else (origin, process, roast date) helps the AI coach give more
            useful suggestions later.
          </p>
        </Card>
      ) : (
        <>
          <BeanList
            beans={active}
            onEdit={openEdit}
            onDelete={(id) => del.mutate(id)}
            onActivate={(id) => activate.mutate(id)}
          />
          {archived.length > 0 && (
            <details className="rounded-md border border-neutral-800 bg-neutral-900/30 p-3 text-sm">
              <summary className="cursor-pointer select-none text-neutral-400">
                {archived.length} archived
              </summary>
              <div className="mt-3">
                <BeanList
                  beans={archived}
                  onEdit={openEdit}
                  onDelete={(id) => del.mutate(id)}
                  onActivate={(id) => activate.mutate(id)}
                  dimmed
                />
              </div>
            </details>
          )}
        </>
      )}
    </div>
  );
}

function BeanList({
  beans,
  onEdit,
  onDelete,
  onActivate,
  dimmed,
}: {
  beans: Bean[];
  onEdit: (b: Bean) => void;
  onDelete: (id: string) => void;
  onActivate: (id: string | null) => void;
  dimmed?: boolean;
}) {
  return (
    <ul className={`space-y-2 ${dimmed ? "opacity-70" : ""}`}>
      {beans.map((b) => (
        <li
          key={b.id}
          className={`flex flex-col gap-2 rounded-md border ${
            b.active ? "border-emerald-700 bg-emerald-950/20" : "border-neutral-800 bg-neutral-900/30"
          } p-3 md:flex-row md:items-start md:justify-between`}
        >
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <span className="text-base font-medium text-neutral-100">{b.name}</span>
              {b.active && (
                <span className="rounded-full border border-emerald-700 bg-emerald-900/40 px-2 py-0.5 text-[10px] uppercase tracking-wider text-emerald-200">
                  In use
                </span>
              )}
            </div>
            <div className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-neutral-400">
              {b.roaster && <span>{b.roaster}</span>}
              {b.origin && <span>{b.origin}</span>}
              {b.process && <span>{b.process}</span>}
              {b.roast_level && <span>{b.roast_level}</span>}
              {b.roast_date && <span>roasted {b.roast_date}</span>}
              {b.default_grind_size && <span>grind {b.default_grind_size}</span>}
              {b.default_grind_rpm != null && <span>{b.default_grind_rpm} RPM</span>}
            </div>
            {b.notes && (
              <p className="mt-1.5 whitespace-pre-wrap text-xs text-neutral-400">{b.notes}</p>
            )}
          </div>
          <div className="flex shrink-0 flex-wrap gap-2">
            {b.active ? (
              <button
                type="button"
                onClick={() => onActivate(null)}
                className="rounded-md border border-emerald-800 bg-emerald-900/30 px-2.5 py-1 text-xs text-emerald-200 hover:bg-emerald-900/50"
                title="Stop auto-tagging new shots with this bag"
              >
                Clear active
              </button>
            ) : (
              !b.archived && (
                <button
                  type="button"
                  onClick={() => onActivate(b.id)}
                  className="rounded-md border border-neutral-700 bg-neutral-900 px-2.5 py-1 text-xs hover:bg-neutral-800"
                  title="Auto-tag new shots with this bag"
                >
                  Set as active
                </button>
              )
            )}
            <button
              type="button"
              onClick={() => onEdit(b)}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-2.5 py-1 text-xs hover:bg-neutral-800"
            >
              Edit
            </button>
            <button
              type="button"
              onClick={() => {
                if (confirm(`Delete "${b.name}"? Shots linked to this bag will keep the reference.`)) {
                  onDelete(b.id);
                }
              }}
              className="rounded-md border border-rose-900 bg-rose-950/40 px-2.5 py-1 text-xs text-rose-200 hover:bg-rose-900/40"
            >
              Delete
            </button>
          </div>
        </li>
      ))}
    </ul>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-xs text-neutral-300">
      <span className="uppercase tracking-wide text-neutral-400">{label}</span>
      {children}
    </label>
  );
}
