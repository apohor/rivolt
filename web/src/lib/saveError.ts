import { ApiError } from "./api";

// saveErrorText is what a failed save says: the server's own sentence when
// it refused the request with a readable reason (a 4xx with {"error": "..."}),
// otherwise plain words. Never a raw error string, stack or HTML page.
export function saveErrorText(err: unknown): string {
  // Pricing a charge that is still open: the server answers 409 with the plain text "charge still in progress" (not JSON), which would
  // otherwise read as a glitch worth retrying.
  if (err instanceof ApiError && err.status === 409 && typeof err.body === "string" && /still in progress/i.test(err.body)) {
    return "This charge is still open, so it can't be priced yet. Rivolt can price it once it ends.";
  }
  if (err instanceof ApiError && err.status >= 400 && err.status < 500) {
    const body = err.body as { error?: unknown } | null;
    const reason = body && typeof body === "object" ? body.error : undefined;
    if (typeof reason === "string" && reason.trim() !== "" && reason.length <= 160 && !/[\n<]/.test(reason)) {
      const s = reason.trim();
      return `Couldn't save: ${s.charAt(0).toUpperCase()}${s.slice(1)}${/[.!?]$/.test(s) ? "" : "."}`;
    }
  }
  return "Couldn't save. Try again in a moment.";
}
