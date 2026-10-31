import { useEffect, useState } from "react";

// Whether this browser has a session, for a public page that offers something only an account can use. /api/auth/me never bounces to
// the login page for a signed-out visitor (it answers 401, or 200 with authenticated:false), and any failure reads as signed out.
export function useSignedIn(): boolean {
  const [yes, setYes] = useState(false);
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const r = await fetch("/api/auth/me", { credentials: "same-origin" });
        if (r.status !== 200) return;
        const body = (await r.json().catch(() => null)) as { authenticated?: boolean } | null;
        if (!cancelled) setYes(!!body && body.authenticated !== false);
      } catch {
        /* signed out */
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);
  return yes;
}
