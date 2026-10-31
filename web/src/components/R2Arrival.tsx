import { useState } from "react";
import { arrivalMonthChoices, arrivalMonthLabel } from "../lib/r2Arrival";
import { useEngagement } from "./AlertSettings";
import { Card } from "./ui";

const DECLINED_KEY = "rivolt.r2arrival.declined";
const declined = (): boolean => {
  try {
    return window.localStorage.getItem(DECLINED_KEY) === "1";
  } catch {
    return false;
  }
};

const btn = "rounded-md border border-neutral-700 px-3 py-1.5 text-sm text-neutral-200 hover:border-neutral-500 disabled:opacity-50";
const quiet = "rounded-md px-3 py-1.5 text-sm text-neutral-400 hover:text-neutral-200";

// "Expecting an R2?": an opt-in, off by default, for ONE email in the month the driver expects delivery. Only a month is kept, it is
// deleted after the email goes, and it can be changed or deleted here at any time. `where` is the /r2 guide (which says "No thanks"
// and stays away once declined) or Settings (which always shows the state).
export function R2ArrivalControl({ where, now }: { where: "guide" | "settings"; now?: Date }) {
  const engagement = useEngagement();
  const choices = arrivalMonthChoices(now);
  const [pick, setPick] = useState(choices[0].value);
  const [changing, setChanging] = useState(false);
  const [gone, setGone] = useState(() => where === "guide" && declined());
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);
  const s = engagement.settings;
  // An older server has no such key: nothing to offer.
  if (!s || s.r2_arrival_month === undefined) return null;
  const set = s.r2_arrival_month;

  async function save(value: string | null, done: string) {
    setMsg(null);
    try {
      await engagement.save({ r2_arrival_month: value });
      setChanging(false);
      setMsg({ tone: "ok", text: done });
    } catch (e) {
      setMsg({ tone: "err", text: e instanceof Error ? e.message : String(e) });
    }
  }

  const picker = (
    <div className="mt-3 flex flex-wrap items-center gap-2">
      <label htmlFor={`r2-month-${where}`} className="text-sm text-neutral-300">
        Expected delivery month
      </label>
      <select id={`r2-month-${where}`} value={pick} onChange={(e) => setPick(e.target.value)} className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1.5 text-sm text-neutral-100">
        {choices.map((c) => (
          <option key={c.value} value={c.value}>
            {c.label}
          </option>
        ))}
      </select>
      <button type="button" disabled={engagement.saving} className={btn} onClick={() => void save(pick, `We'll send one email in ${arrivalMonthLabel(pick)}.`)}>
        {set ? "Save month" : "Remind me that month"}
      </button>
      {set && changing && (
        <button type="button" className={quiet} onClick={() => setChanging(false)}>
          Cancel
        </button>
      )}
      {!set && where === "guide" && (
        <button
          type="button"
          className={quiet}
          onClick={() => {
            try {
              window.localStorage.setItem(DECLINED_KEY, "1");
            } catch {
              /* hides for this visit only */
            }
            setGone(true);
          }}
        >
          No thanks
        </button>
      )}
    </div>
  );

  const status = msg && (
    <p role={msg.tone === "err" ? "alert" : "status"} className={`mt-2 text-xs ${msg.tone === "err" ? "text-rose-400" : "text-emerald-300"}`}>
      {msg.text}
    </p>
  );

  if (set) {
    return (
      <Card title={where === "settings" ? "R2 delivery reminder" : undefined} id={where === "settings" ? "r2-arrival" : undefined}>
        <div data-r2-arrival="set">
          <p className="text-sm text-neutral-100">
            R2 delivery reminder: <span className="font-medium">{arrivalMonthLabel(set)}</span>
          </p>
          <p className="mt-1 text-xs text-neutral-500">One email that month with the connect steps, then the month is deleted.</p>
          {changing ? (
            picker
          ) : (
            <div className="mt-2 flex gap-2">
              <button type="button" className={btn} onClick={() => { setPick(set); setChanging(true); }}>
                Change
              </button>
              <button type="button" disabled={engagement.saving} className={btn} onClick={() => void save(null, "Reminder removed.")}>
                Delete
              </button>
            </div>
          )}
          {status}
        </div>
      </Card>
    );
  }
  if (gone) return null;
  return (
    <Card title={where === "settings" ? "R2 delivery reminder" : undefined} id={where === "settings" ? "r2-arrival" : undefined}>
      <div data-r2-arrival="unset">
        <h2 className="text-base font-semibold text-neutral-100">Expecting an R2?</h2>
        <p className="mt-1 text-sm text-neutral-400">
          Tell us the month you expect delivery and we&apos;ll send one email that month with the connect steps. Nothing else, ever; change or delete the month in Settings, and we delete it after the email goes.
        </p>
        {picker}
        {status}
      </div>
    </Card>
  );
}
