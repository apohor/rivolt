// The words of Settings → Security → Apple (Link Apple to my account), in one place. From growth's copy (_shared/apple-link-copy.md, DA-approved
// Nov 1, 2026); ux places them. The lines marked "not in growth's list" were mine; growth reviewed them (§6 of the copy file) and four were reworded.
export const APPLE_LINK_COPY = {
  title: "Apple",

  // Not connected.
  connect: "Connect Apple",
  connectLine: "Lets you sign in with your Apple ID, including one that hides your email. Your password is asked for first.",
  connectPromptTitle: "Connect Apple to this account",
  passwordLabel: "Your Rivolt password",
  continueToApple: "Continue to Apple",
  cancel: "Cancel",

  // Connected.
  connected: "Apple: connected",
  connectedLine: "You can sign in to this account with your Apple ID. Your email on Rivolt stays as it is.",
  disconnect: "Disconnect",
  disconnectTitle: "Disconnect Apple?",
  disconnectBody:
    "Your Apple ID will no longer sign in to this account, and we ask Apple to forget Rivolt. Your password and any passkeys keep working. You can connect Apple again later.",
  confirmDisconnect: "Disconnect Apple",
  keep: "Keep it",
  // Connected, and the account has no other way in (in v1: no password): the button is disabled and this is why.
  cannotDisconnect: "Apple is the only way into this account, so it can't be disconnected.",

  // The refusal for an account with no password (`password_needed_to_link`): it signs in only with Google or GitHub.
  needsPasswordTitle: "Apple can't be connected to this account yet",
  needsPasswordBody:
    "Connecting Apple needs your Rivolt password typed in, and this account signs in only with Google or GitHub and has none. We don't yet have a way to add a password to an account like this, so Apple linking isn't available for it for now. Signing in with Google or GitHub keeps working.",

  // What came back from Apple, by the ?apple= code on the return to Settings.
  returned: {
    linked: "Apple connected. You can now sign in with your Apple ID.",
    apple_in_use:
      "This Apple ID is already connected to a different Rivolt account. If that is a second account you made by mistake, sign in to it with Apple, delete it from its Settings, then come back here and connect Apple.",
    apple_linked: "This account is already connected to an Apple ID. Disconnect it first to connect a different one.",
    session_expired: "That took too long or the session ended. Start again from Settings.",
    verification_failed: "Apple did not complete the sign-in. Try again in a moment.",
    provider_error: "Apple did not complete the sign-in. Try again in a moment.",
    server_error: "Something went wrong on our side. Try again in a moment, or email support@rivolt.dev.",
  } as Record<string, string>,
  disconnected: "Apple disconnected.",

  // Progress (not in growth's list).
  connecting: "Opening Apple…",
  disconnecting: "Disconnecting…",
  loadFailed: "Couldn't load your sign-in methods. Try again.",

  // Failures of connect and disconnect, by the server's error code (not in growth's list, except the refusal above).
  failures: {
    password_incorrect: "That password isn't right.",
    password_required: "Type your password to confirm.",
    apple_not_available: "Sign in with Apple isn't available on this server.",
    not_linked: "Apple isn't connected to this account.",
    too_many: "Too many attempts. Wait a few minutes, then try again.",
    not_here: "Use a browser signed in as yourself to change this. It can't be done from the iPhone app or while viewing as another user.",
    generic: "That didn't work. Try again in a moment.",
  },

  // Under "Continue with Apple" on the sign-in page.
  signInNote:
    "Already have a Rivolt account? Sign in the way you did before, then connect Apple in Settings. Signing in with Apple here, with your email hidden, would make a new, empty account.",
} as const;
