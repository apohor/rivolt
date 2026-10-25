# A compromised account

When someone other than the owner has hold of a Rivolt account (a known password, a hijacked session, a passkey the owner did not
add), there are two ways back. The owner does it themselves, or support does it for them.

## The owner, from a browser they are signed in on

Settings → Security has two actions (the second one is the "secure" action everywhere in the code; people only ever see its label). Both ask for the current password first (wrong guesses are counted per address and per client,
ten per fifteen minutes) and email the address on file every time.

- **Change password**: sets the new password; signs every other browser and the iPhone app out; forgets the sign-in the identity
  provider remembers for them; keeps the passkeys and lists them, so they can remove any they do not recognise.
- **Reset password and remove all passkeys**: all of that, and removes **every** passkey, including their own.

The "a passkey was added" email points here.

## Support, when the owner cannot sign in

Use the admin site (admin.rivolt.dev, behind Access): Users → the account → **Secure this account** (support's own button)
(`POST /api/admin/users/{id}/secure`; the button is ux's, and until it ships the same call is made from the admin session). It is one audited action that:

1. sets a new random password through Kratos;
2. forgets the identity provider's remembered logins and Kratos sessions (without this a remembered browser signs straight back in);
3. revokes every Rivolt session and iPhone app token;
4. deletes every passkey;
5. emails the account "Rivolt support secured your account".

The password is changed **first**, on purpose: it kills the one the other person knows before anything else. If a later step fails, the
account is left holding a random password that nobody knows, which is the safe state for a compromised account; run the action
again to finish. The new password is shown **once** on that screen (and returned once by the API). It is not logged, not in the audit row, not in
the email. If any step fails the screen says which, shows **no** password, and the action can simply be run again: every step is
safe to repeat.

### Who runs it, and the rules for handing the password over

- **Only the operator runs this on prod and handles the temporary password.** An agent never runs it, never sees the password, and
  never judges proof of ownership. (The prod admin site has no service tokens, so an agent cannot call it; that is by design.)
- Send the password **only to the email address on the account**. Never to an address, phone number or chat that the person asking
  gives you, even if they say they lost access to the mailbox: that is the case this rule exists for. If the mailbox is lost too, the
  operator decides whether the person has proved ownership some other way; there is no "second approver", so be slow and say no.
- **Never paste the temporary password into a ticket, a chat or the audit note.** Read it from the screen and send it by email only.
- Say nothing about the account to the requester before then (not whether it exists, not what it holds).
- Tell the owner to change the password at once in Settings → Security (**Change password** with the temporary one as the current
  password), and to remove any passkey they do not recognise.
- Note the time and who asked in the support ticket (never the password). The audit log has the action, who ran it, and the target.

### After

Check the audit log shows `user.secure` for the account. If the account was an admin or has the Rivian connection, also look at the
Rivian session (a password change does not touch it) and decide with the owner whether to reconnect.

## How the remembered sign-in is cleared, and how to check it

Rivolt's own sessions are not the whole story: Hydra remembers an accepted login (24 hours on prod), and a remembered browser is
signed in again with **no password**. Every revoke above also deletes Hydra's login and consent sessions for the account and
Kratos' sessions for the identity. To check on preview after a change: sign in in two browsers, run the action from one, click
Sign in in the other. It must show the password form.
