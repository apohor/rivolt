import { useCallback, useEffect, useState } from "react";

// The text of a legal or guide page (src/content/*.md, imported with ?url): a content-hashed asset the page fetches when it opens, not part of
// any script, so the page's chunk stays small. Texts already fetched stay for the session.
const loaded = new Map<string, string>();

export type LegalText = { text: string | null; failed: boolean; retry: () => void };

export function useLegalText(url: string | undefined): LegalText {
  const [text, setText] = useState<string | null>(url ? (loaded.get(url) ?? null) : null);
  const [failed, setFailed] = useState(false);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    if (!url) return;
    const hit = loaded.get(url);
    if (hit !== undefined) {
      setText(hit);
      return;
    }
    let cancelled = false;
    setFailed(false);
    fetch(url)
      .then((r) => (r.ok ? r.text() : Promise.reject(new Error(String(r.status)))))
      .then((t) => {
        loaded.set(url, t);
        if (!cancelled) setText(t);
      })
      .catch(() => {
        if (!cancelled) setFailed(true);
      });
    return () => {
      cancelled = true;
    };
  }, [url, attempt]);
  const retry = useCallback(() => setAttempt((n) => n + 1), []);
  return { text, failed, retry };
}
