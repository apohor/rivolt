import { ApiError } from "./api";

// What to tell the person when the password change or the full reset did not work. The server's own text is never shown.
// `incomplete` names what a changed password left undone: "sign_out" (other devices still signed in) and, after the full reset,
// "passkeys" (some still not removed).
export type SecurityFailure = { text: string; noPassword?: boolean; signOutIncomplete?: boolean; passkeysIncomplete?: boolean };

export function securityFailure(e: unknown): SecurityFailure {
  if (e instanceof ApiError) {
    const body = (e.body ?? {}) as { error?: string; reason?: string; password_changed?: boolean; incomplete?: string[] };
    if (e.status === 403 && body.error === "password_incorrect") return { text: "That isn't your current password." };
    if (e.status === 429) return { text: "Too many wrong attempts. Wait a few minutes, then try again." };
    // The policy's own reason is never shown (it is the identity service's wording); it only picks which of our lines to say.
    if (e.status === 422 && body.error === "password_rejected") return { text: rejectedPassword(body.reason) };
    if (e.status === 400 && body.error === "new_password_must_differ") return { text: "The new password must be different from the current one." };
    if (e.status === 409 && body.error === "needs_an_email_account")
      return { text: "This account signs in with Google, GitHub or Apple, so it has no Rivolt password to change.", noPassword: true };
    if (e.status === 502 && body.error === "password_changed_but_sign_out_incomplete") {
      const left = Array.isArray(body.incomplete) ? body.incomplete : ["sign_out"];
      const signOut = left.includes("sign_out") || !left.includes("passkeys");
      const passkeys = left.includes("passkeys");
      const parts = [
        signOut ? "signing out your other devices didn't finish (use “Sign out everywhere else” under Signed-in devices to finish it)" : "",
        passkeys ? "removing your passkeys didn't finish (use “Remove all passkeys” below)" : "",
      ].filter(Boolean);
      return { text: `Your password was changed, and the old one no longer works, but ${parts.join(" and ")}.`, signOutIncomplete: signOut, passkeysIncomplete: passkeys };
    }
    if (e.status === 403) return { text: "This can't be done from here (the iPhone app, or viewing as another user). Use a browser signed in as yourself." };
  }
  return { text: "That didn't work. Try again." };
}

export function rejectedPassword(reason: unknown): string {
  const r = typeof reason === "string" ? reason.toLowerCase() : "";
  if (/short|length|at least|characters/.test(r)) return "That new password is too short. Use a longer one.";
  if (/breach|leak|common|commonly|compromised|pwned/.test(r)) return "That new password is too common. Use one you don't use anywhere else.";
  if (/similar|identifier|email|username/.test(r)) return "That new password is too close to your email address. Use something different.";
  return "That new password isn't accepted. Use a longer one that is less common and that you don't use anywhere else.";
}

export const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;
