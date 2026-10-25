import { ApiError } from "./api";

// WebAuthn in the browser, with no library: the server's ceremony options and the browser's credentials are both the standard JSON
// forms, which current browsers read and write themselves (parse...OptionsFromJSON and toJSON). Where they are missing the
// passkey controls stay hidden.

type PKC = typeof PublicKeyCredential & {
  parseCreationOptionsFromJSON?: (o: unknown) => PublicKeyCredentialCreationOptions;
  parseRequestOptionsFromJSON?: (o: unknown) => PublicKeyCredentialRequestOptions;
};

export function passkeysSupported(): boolean {
  if (typeof window === "undefined" || typeof navigator === "undefined" || !navigator.credentials || typeof PublicKeyCredential === "undefined") return false;
  const pkc = PublicKeyCredential as PKC;
  return typeof pkc.parseCreationOptionsFromJSON === "function" && typeof pkc.parseRequestOptionsFromJSON === "function";
}

// Makes a new passkey from the server's creation options; null when the person cancelled the browser's prompt.
export async function createPasskey(options: { publicKey: unknown }): Promise<unknown | null> {
  const pkc = PublicKeyCredential as PKC;
  const publicKey = pkc.parseCreationOptionsFromJSON!(options.publicKey);
  try {
    const cred = (await navigator.credentials.create({ publicKey })) as PublicKeyCredential | null;
    return cred ? cred.toJSON() : null;
  } catch (e) {
    if (isCancelled(e)) return null;
    throw e;
  }
}

// Signs in with a passkey from the server's request options; null when the person cancelled.
export async function getPasskey(options: { publicKey: unknown }): Promise<unknown | null> {
  const pkc = PublicKeyCredential as PKC;
  const publicKey = pkc.parseRequestOptionsFromJSON!(options.publicKey);
  try {
    const cred = (await navigator.credentials.get({ publicKey })) as PublicKeyCredential | null;
    return cred ? cred.toJSON() : null;
  } catch (e) {
    if (isCancelled(e)) return null;
    throw e;
  }
}

// The person dismissed the prompt, or the browser refused (not allowed, aborted, no matching passkey): not an error to report.
function isCancelled(e: unknown): boolean {
  return e instanceof DOMException && (e.name === "NotAllowedError" || e.name === "AbortError");
}

// What to tell the person when adding a passkey did not work. Never the server's own text.
export function addFailureMessage(e: unknown): string {
  if (e instanceof ApiError) {
    const err = (e.body as { error?: string } | null | undefined)?.error;
    if (e.status === 403 && err === "password_incorrect") return "That password isn't right.";
    if (e.status === 409 && err === "passkeys_need_an_email_account")
      return "Passkeys are for accounts that sign in with an email and password. This account signs in with Google, GitHub or Apple, so use that provider's own passkey.";
    if (e.status === 409 && err === "too_many_passkeys") return "You have the most passkeys an account can hold. Remove one first.";
    if (e.status === 429) return "Too many attempts. Wait a few minutes, then try again.";
    if (e.status === 403) return "Passkeys can't be changed from here (the iPhone app, or viewing as another user). Use a browser signed in as yourself.";
  }
  return "Couldn't add the passkey. Try again.";
}

export function signInFailureMessage(e: unknown): string {
  if (e instanceof ApiError && e.status === 429) return "Too many attempts. Wait a few minutes, then try again.";
  if (e instanceof ApiError && e.status === 401) return "That passkey isn't registered to an account any more. Sign in with your email and password.";
  return "Couldn't sign in with a passkey. Use your password instead.";
}
