// The words of Settings → Security → "Sign in with Apple" (Link Apple to my account), in one place so the final copy drops in without touching
// the logic. PLACEHOLDER: growth is writing the real text (the connect and disconnect lines, the refusal for an account with no password, and
// the privacy.md lines with the date bump); ux reviews it. Keep the keys; change only the strings.
export const APPLE_LINK_COPY = {
  title: "Sign in with Apple",
  intro: "Connect your Apple ID to sign in to this account with Apple.",
  notLinked: "Apple isn't connected to this account.",
  linked: "Apple is connected to this account.",
  passwordLabel: "Your password, to confirm it's you",
  connect: "Connect Apple",
  connecting: "Opening Apple…",
  disconnect: "Disconnect Apple",
  // The account is connected to Apple but has no password (it was created by signing in with Apple): it cannot disconnect from here.
  cannotDisconnect: "Disconnecting Apple needs an account that signs in with an email and password.",
  disconnecting: "Disconnecting…",
  confirmDisconnect: "Yes, disconnect",
  cancel: "Cancel",
  // The account has no password: it signs in with Google or GitHub only (the refusal behind `password_needed_to_link`).
  needsPassword: "Connecting Apple needs an account that signs in with an email and password. Accounts that use only Google or GitHub can't connect Apple yet.",
  // What came back from Apple, by the ?apple= code on the return to Settings.
  returned: {
    linked: "Apple is connected.",
    apple_in_use: "That Apple ID is already connected to a different Rivolt account.",
    apple_linked: "This account is already connected to a different Apple ID. Disconnect it first.",
    session_expired: "That took too long, or you signed out. Try connecting again.",
    verification_failed: "Apple didn't share enough to connect. Try again.",
    provider_error: "Couldn't connect with Apple. Try again.",
    server_error: "Something went wrong on our end. Try again.",
  } as Record<string, string>,
  disconnected: "Apple is disconnected.",
  // Failures of connect and disconnect, by the server's error code.
  failures: {
    password_incorrect: "That password isn't right.",
    password_required: "Type your password to confirm.",
    password_needed_to_link: "Connecting Apple needs an account that signs in with an email and password. Accounts that use only Google or GitHub can't connect Apple yet.",
    apple_not_available: "Sign in with Apple isn't available here.",
    not_linked: "Apple isn't connected to this account.",
    too_many: "Too many attempts. Wait a few minutes, then try again.",
    not_here: "This can't be changed from here (the iPhone app, or viewing as another user). Use a browser signed in as yourself.",
    generic: "Couldn't do that. Try again.",
  },
} as const;
