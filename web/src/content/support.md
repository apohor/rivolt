# Support

Rivolt is run by ZeroDowntime LLC. Email [support@rivolt.dev](mailto:support@rivolt.dev); we aim to reply within a few days. Requests about your data are handled within 30 days, as the [Privacy Policy](/privacy) says. Include the page or screen you were on, never your password.

Bug reports and feature requests are also welcome on [GitHub](https://github.com/apohor/rivolt/issues). Issues are public, so leave out your VIN, address, location and email address.

## Common questions

**What is Rivolt?**
An open-source companion for Rivian owners, free during the beta. It records your truck's drives and charges, prices each charge, using what Rivian billed where it billed, otherwise your rate or a labelled estimate, and shows where each figure came from. It works on the web at rivolt.dev and as an iPhone app.

**Is it official?**
No. Rivolt is independent and is not affiliated with, endorsed by or sponsored by Rivian. It reads data through Rivian's unofficial API, which Rivian can change or block at any time, so Rivolt can break.

**How do I connect my truck?**
Sign in to rivolt.dev and add your Rivian account on the Connect step. We recommend adding a second Rivian account as an Authorized Driver and connecting that one, so you can remove Rivolt from the Rivian app at any time. Your Rivian password is used once to sign in and is never stored; what we keep is your Rivian email and an encrypted session token. Details: [Is it safe to connect?](/safe-to-connect)

**I connected, but I don't see my truck or any data.**
The usual cause is a driver account whose invite has not been accepted yet: sign in to the Rivian app on a phone with that account once, then tap Check again in Rivolt. Drives and charges appear as the truck is used.

**Does Rivolt wake my truck?**
Rivolt sends no commands to your truck (no locks, climate or charging controls). Reading its data can occasionally wake it.

**How do I disconnect Rivolt from my Rivian?**
In Settings → Account, choose Sign out of Rivian; Rivolt stops reading your truck. If you connected an Authorized Driver account, you can also remove that driver in the Rivian app, which cuts access from Rivian's side.

**I signed in with Apple and got an empty account.**
Apple's Hide My Email gives us a relay address, so a first Apple sign-in makes a new account rather than joining your existing one. There is no way to merge the two. To fix it: sign in to the new account with Apple, delete it from Settings → Data (that also tells Apple to forget it), sign in to your real account the way you did before, and use Settings → Sign-in methods → Connect Apple. From then on, Apple signs in to your real account. Connecting Apple needs your Rivolt password; an account that signs in only with Google or GitHub can't connect Apple yet.

**Where is my data, and how do I export or delete it?**
Your data is stored on servers we run in the United States. On rivolt.dev, open Settings → Data to download everything as a zip of CSV files or a full JSON backup, or to delete your account. Deleting removes your data; copies can remain in logs for up to 90 days and in backups for up to a year, as the Privacy Policy describes. The iPhone app links to the same page.

## Legal

[Privacy Policy](/privacy) · [Terms](/terms) · [Source code (MIT)](https://github.com/apohor/rivolt)

Rivolt is unofficial, not affiliated with Rivian.
