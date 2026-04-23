import { useEffect, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { backend, MachineError } from "../lib/api";
import { Card } from "./ui";

// ShotFeedbackCard lets the user attach a 1..5 star rating and a free-form
// tasting note to a shot. Both fields are optional and independent: you
// can save a note without a rating, or a rating without a note.
export default function ShotFeedbackCard({
  shotId,
  initialRating,
  initialNote,
  initialBeanId,
  initialGrind,
  initialGrindRPM,
}: {
  shotId: string;
  initialRating: number | null;
  initialNote: string;
  initialBeanId?: string;
  initialGrind?: string;
  initialGrindRPM?: number | null;
}) {
  const qc = useQueryClient();
  const [rating, setRating] = useState<number | null>(initialRating);
  const [note, setNote] = useState<string>(initialNote);
  const [beanId, setBeanId] = useState<string>(initialBeanId ?? "");
  const [grind, setGrind] = useState<string>(initialGrind ?? "");
  // Keep RPM as a string so the user can type partial values (e.g. "80"
  // while intending "800") without us prematurely coercing — parse on save.
  const [rpm, setRpm] = useState<string>(
    initialGrindRPM != null ? String(initialGrindRPM) : "",
  );

  // If the query refetches (e.g. after sync) and brings fresher values,
  // reset the draft — unless the user has local changes.
  useEffect(() => {
    setRating(initialRating);
    setNote(initialNote);
    setBeanId(initialBeanId ?? "");
    setGrind(initialGrind ?? "");
    setRpm(initialGrindRPM != null ? String(initialGrindRPM) : "");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shotId]);

  const initialRpmStr = initialGrindRPM != null ? String(initialGrindRPM) : "";
  const dirty =
    rating !== initialRating ||
    note !== initialNote ||
    beanId !== (initialBeanId ?? "") ||
    grind !== (initialGrind ?? "") ||
    rpm !== initialRpmStr;

  // Load beans for the picker. Cheap query, cached globally.
  const beans = useQuery({
    queryKey: ["beans"],
    queryFn: () => backend.listBeans(),
    staleTime: 30_000,
  });
  const activeBeans = (beans.data ?? []).filter(
    (b) => !b.archived || b.id === beanId,
  );
  // Show the selected bag's default grind / RPM as placeholders so the
  // user can see what they'd inherit without having to type it. If they
  // leave the input blank and the shot already has a value stored,
  // Submitting won't change it; if the shot is unset, the placeholder
  // hints at the bean's preferred setting.
  const selectedBean = (beans.data ?? []).find((b) => b.id === beanId);
  const defaultGrindHint = selectedBean?.default_grind_size ?? "";
  const defaultRpmHint =
    selectedBean?.default_grind_rpm != null
      ? String(selectedBean.default_grind_rpm)
      : "";

  const save = useMutation({
    mutationFn: async () => {
      await backend.setShotFeedback(shotId, rating, note);
      if (beanId !== (initialBeanId ?? "")) {
        await backend.setShotBean(shotId, beanId);
      }
      if (grind !== (initialGrind ?? "") || rpm !== initialRpmStr) {
        // Empty grind is allowed (clears the label). RPM is nullable;
        // an empty string or NaN → null (clears the stored value).
        const parsed = rpm.trim() === "" ? NaN : Number(rpm);
        const rpmVal = Number.isFinite(parsed) ? parsed : null;
        await backend.setShotGrind(shotId, grind.trim(), rpmVal);
      }
    },
    onSuccess: async () => {
      await qc.invalidateQueries({ queryKey: ["shot", shotId] });
      await qc.invalidateQueries({ queryKey: ["shots"] });
    },
  });

  // --- Voice capture + transcription --------------------------------------
  // We record with the browser's MediaRecorder and POST the resulting
  // Blob to /api/ai/transcribe (Whisper). The returned text is appended
  // to whatever is already in the note textarea so users can dictate
  // incrementally without clobbering typed content.
  const recRef = useRef<MediaRecorder | null>(null);
  const chunksRef = useRef<Blob[]>([]);
  const streamRef = useRef<MediaStream | null>(null);
  const [recording, setRecording] = useState(false);
  const [micError, setMicError] = useState<string | null>(null);

  const transcribe = useMutation({
    mutationFn: (blob: Blob) => backend.transcribe(blob),
    onSuccess: (res) => {
      const add = res.text.trim();
      if (!add) return;
      setNote((prev) => (prev ? prev.replace(/\s*$/, "") + "\n" + add : add));
    },
  });

  async function startRecording() {
    setMicError(null);
    if (!navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === "undefined") {
      setMicError("This browser can't record audio.");
      return;
    }
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      streamRef.current = stream;
      // Pick the first supported mime; Safari's MediaRecorder only does
      // mp4/aac, Chrome/Firefox prefer webm/opus. Whisper accepts both.
      const candidates = [
        "audio/webm;codecs=opus",
        "audio/webm",
        "audio/mp4",
        "audio/ogg;codecs=opus",
      ];
      let mimeType: string | undefined;
      for (const c of candidates) {
        if (MediaRecorder.isTypeSupported(c)) { mimeType = c; break; }
      }
      const rec = mimeType ? new MediaRecorder(stream, { mimeType }) : new MediaRecorder(stream);
      recRef.current = rec;
      chunksRef.current = [];
      rec.ondataavailable = (e) => {
        if (e.data.size > 0) chunksRef.current.push(e.data);
      };
      rec.onstop = () => {
        const blob = new Blob(chunksRef.current, { type: rec.mimeType || "audio/webm" });
        chunksRef.current = [];
        streamRef.current?.getTracks().forEach((t) => t.stop());
        streamRef.current = null;
        recRef.current = null;
        setRecording(false);
        if (blob.size > 0) transcribe.mutate(blob);
      };
      rec.start();
      setRecording(true);
    } catch (err) {
      setMicError(err instanceof Error ? err.message : String(err));
      streamRef.current?.getTracks().forEach((t) => t.stop());
      streamRef.current = null;
    }
  }

  function stopRecording() {
    recRef.current?.stop();
  }

  // Safety net: if the component unmounts mid-recording, release the mic.
  useEffect(() => {
    return () => {
      recRef.current?.state === "recording" && recRef.current.stop();
      streamRef.current?.getTracks().forEach((t) => t.stop());
    };
  }, []);

  return (
    <Card title="Your notes">
      <div className="space-y-3">
        <div className="flex items-center gap-2">
          <span className="text-xs uppercase tracking-wide text-neutral-500">Beans</span>
          <select
            value={beanId}
            onChange={(e) => setBeanId(e.target.value)}
            className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
          >
            <option value="">—</option>
            {activeBeans.map((b) => (
              <option key={b.id} value={b.id}>
                {b.name}
                {b.roaster ? ` · ${b.roaster}` : ""}
                {b.archived ? " (archived)" : ""}
              </option>
            ))}
          </select>
          <a
            href="/beans"
            className="ml-auto text-xs text-neutral-500 underline hover:text-neutral-300"
          >
            Manage beans
          </a>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs uppercase tracking-wide text-neutral-500">Grind</span>
          <input
            type="text"
            value={grind}
            onChange={(e) => setGrind(e.target.value)}
            placeholder={defaultGrindHint || "e.g. 2.8, 12 clicks"}
            className="w-28 rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
          />
          <span className="ml-2 text-xs uppercase tracking-wide text-neutral-500">RPM</span>
          <input
            type="number"
            inputMode="numeric"
            min={0}
            step={10}
            value={rpm}
            onChange={(e) => setRpm(e.target.value)}
            placeholder={defaultRpmHint || "800"}
            className="w-20 rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-sm text-neutral-100"
          />
          {(defaultGrindHint || defaultRpmHint) &&
          (grind === "" || rpm === "") ? (
            <button
              type="button"
              onClick={() => {
                if (grind === "" && defaultGrindHint) setGrind(defaultGrindHint);
                if (rpm === "" && defaultRpmHint) setRpm(defaultRpmHint);
              }}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-2 py-1 text-xs text-neutral-300 hover:bg-neutral-800"
              title="Copy the bag's default grind settings into this shot"
            >
              Use bag defaults
            </button>
          ) : (
            <span className="text-xs text-neutral-500">
              {rpm ? "RPM — variable-speed grinders only" : "optional"}
            </span>
          )}
        </div>

        <div className="flex items-center gap-2">
          <span className="text-xs uppercase tracking-wide text-neutral-500">Rating</span>
          <div className="flex items-center gap-0.5">
            {[1, 2, 3, 4, 5].map((n) => (
              <button
                key={n}
                type="button"
                onClick={() => setRating(n === rating ? null : n)}
                aria-label={`${n} star${n === 1 ? "" : "s"}`}
                className="p-0.5 text-lg leading-none text-amber-400 hover:scale-110"
              >
                {rating != null && n <= rating ? "★" : "☆"}
              </button>
            ))}
            {rating != null && (
              <button
                type="button"
                onClick={() => setRating(null)}
                className="ml-2 text-xs text-neutral-500 hover:text-neutral-300"
              >
                clear
              </button>
            )}
          </div>
        </div>

        <div>
          <div className="mb-1 flex items-center justify-between">
            <label className="text-xs uppercase tracking-wide text-neutral-500">
              Note
            </label>
            <div className="flex items-center gap-2">
              {transcribe.isPending && (
                <span className="text-xs text-neutral-400">transcribing…</span>
              )}
              <button
                type="button"
                onClick={recording ? stopRecording : startRecording}
                disabled={transcribe.isPending}
                aria-label={recording ? "Stop recording" : "Record a voice note"}
                title={recording ? "Stop recording" : "Record a voice note (transcribed by AI)"}
                className={
                  "flex items-center gap-1.5 rounded-md border px-2 py-1 text-xs transition disabled:opacity-40 " +
                  (recording
                    ? "border-red-700 bg-red-950/60 text-red-200 hover:bg-red-900/60"
                    : "border-neutral-700 bg-neutral-900 text-neutral-200 hover:bg-neutral-800")
                }
              >
                <span
                  className={
                    recording
                      ? "h-2 w-2 animate-pulse rounded-full bg-red-400"
                      : "h-2 w-2 rounded-full bg-neutral-500"
                  }
                />
                {recording ? "Stop" : "Record note"}
              </button>
            </div>
          </div>
          <textarea
            value={note}
            onChange={(e) => setNote(e.target.value)}
            placeholder="Tasting notes, grind setting, bean, anything worth remembering…"
            rows={4}
            className="w-full rounded-md border border-neutral-800 bg-neutral-900 p-3 text-sm text-neutral-100 placeholder-neutral-600 focus:border-neutral-600 focus:outline-none"
          />
          {micError && (
            <div className="mt-1 text-xs text-red-300">{micError}</div>
          )}
          {transcribe.error && (
            <div className="mt-1 text-xs text-red-300">
              Transcription failed:{" "}
              {transcribe.error instanceof MachineError
                ? (transcribe.error.body as { error?: string })?.error ?? transcribe.error.message
                : String(transcribe.error)}
            </div>
          )}
        </div>

        <div className="flex items-center gap-2">
          <button
            type="button"
            disabled={!dirty || save.isPending}
            onClick={() => save.mutate()}
            className="rounded-md bg-emerald-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-emerald-500 disabled:opacity-40"
          >
            {save.isPending ? "saving…" : "Save"}
          </button>
          {dirty && (
            <button
              type="button"
              onClick={() => {
                setRating(initialRating);
                setNote(initialNote);
                setBeanId(initialBeanId ?? "");
              }}
              className="rounded-md border border-neutral-700 bg-neutral-900 px-3 py-1.5 text-sm text-neutral-300 hover:bg-neutral-800"
            >
              Discard
            </button>
          )}
          {save.isSuccess && !dirty && (
            <span className="text-xs text-emerald-400">Saved.</span>
          )}
        </div>

        {save.error && (
          <div className="rounded-md border border-red-900 bg-red-950/30 px-3 py-2 text-xs text-red-200">
            {save.error instanceof MachineError
              ? `${save.error.status}: ${JSON.stringify(save.error.body)}`
              : String(save.error)}
          </div>
        )}
      </div>
    </Card>
  );
}
