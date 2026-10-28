// What the sign-in pages remember across a restart or a reload: the email that was typed (never the password) and which provider
// the person chose, in sessionStorage so it goes with the tab and never lasts past it. A sign-in that expired, a flow started in
// another tab, or a reload after a deploy (asset-guard) then lands back with the email filled in. Storage may throw (private
// windows, blocked site data); then nothing is remembered and nothing breaks.

const EMAIL_KEY = "rivolt.signin.email";
const PROVIDER_KEY = "rivolt.signin.provider";

function get(key: string): string {
  try {
    return window.sessionStorage.getItem(key) ?? "";
  } catch {
    return "";
  }
}

function set(key: string, value: string) {
  try {
    if (value) window.sessionStorage.setItem(key, value);
    else window.sessionStorage.removeItem(key);
  } catch {
    /* ignore */
  }
}

export const readSigninEmail = (): string => get(EMAIL_KEY);
export const rememberSigninEmail = (email: string) => set(EMAIL_KEY, email.trim());
export const forgetSigninEmail = () => set(EMAIL_KEY, "");
export const readSigninProvider = (): string => get(PROVIDER_KEY);
export const rememberSigninProvider = (name: string) => set(PROVIDER_KEY, name);

// The words under a sign-in that could not be finished because its state was gone.
export const SESSION_EXPIRED_TEXT = "That sign-in took too long or was started in another tab.";

// The provider to start again: the one chosen last when the server still lists it, else none (the person picks).
export function providerToRestart<T extends { name: string }>(providers: T[] | null, remembered: string): T | null {
  if (!providers || !remembered) return null;
  return providers.find((p) => p.name === remembered) ?? null;
}
