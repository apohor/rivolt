// signup_v2: the sample truck remembers, for the tab, that the visitor came from their own account (?from=app). Signing out ends that.
export const FROM_APP_KEY = "rivolt.demo_from_app";

export function forgetFromApp() {
  try {
    sessionStorage.removeItem(FROM_APP_KEY);
  } catch {
    /* storage blocked */
  }
}
