# Is it safe to connect my Rivian account to Rivolt?

Rivolt asks for a Rivian sign-in so it can read your truck's drives and charging. That is a real decision, so here is what Rivolt does with it, in plain terms, and what the risks are. Rivolt is an open-source companion for Rivian owners, free during the beta. It is unofficial and not affiliated with, endorsed by or sponsored by Rivian.

## Look first, decide later

You don't need an account to see what Rivolt does. The [demo](https://demo.rivolt.dev) shows every tab on a sample truck with generated data: no sign-in, no Rivian account. If you then want to see a planned trip, the demo's [Plan tab](https://demo.rivolt.dev/demo/trips/plan) has sample trips, including Los Angeles to Las Vegas in two days.

## What Rivolt reads, and what it never does

- Connect a Rivian account and Rivolt reads your truck's data, such as drives, charges, battery and location.
- It sends no commands to your truck (no locks, climate or charging controls). Reading its data can occasionally wake it.
- Your Rivian password is used once to sign in and is never stored. What Rivolt keeps is your Rivian email, an encrypted session token, and when you signed in (so it can warn you before Rivian ends the session).
- The drives and charges it records, including location, are described in the [Privacy policy](/privacy): what is recorded, how long it is kept and how to delete it. There are no ads and no third-party analytics.

## We recommend a separate Authorized Driver account

You don't have to give Rivolt your main login. Create a second, free Rivian account, add it to your vehicle as an Authorized Driver in the Rivian app, and connect that one.

- Rivolt then holds that account's session, not yours.
- To remove Rivolt, remove the driver in the Rivian app. You don't need to change your main password.
- Rivolt's requests count against the dedicated account, not your main one.

Trade-offs: you need a second email address and a few minutes in the Rivian app, and an Authorized Driver account may not see all of the history your main account holds, such as charges the owner started. Live data from the moment you connect is complete. You can still connect your main account if you prefer. The [step-by-step guide](https://github.com/apohor/rivolt/blob/main/docs/SIGNUP.md#recommended-connect-a-dedicated-authorized-driver-account) walks through it. If Rivolt finds no vehicle after you connect, the usual cause is a driver invite that has not been accepted yet: sign in to the Rivian app on a phone with that account once, then tap Check again.

## How to undo it

- In Rivolt, Settings → Account → Rivian account → Sign out of Rivian. The stored session is deleted and Rivolt stops reading your truck.
- Or remove the Authorized Driver in the Rivian app, which cuts access from Rivian's side.
- You can export your data as a zip of CSVs (charges with cost source, drives and trips) or a full JSON backup, and delete your account, from Settings on rivolt.dev.

## The honest risks

- Rivolt is unofficial. It uses Rivian's unofficial API, so it can break, and Rivian can change or block access at any time, with either kind of account.
- Any service that holds a session token carries some risk. That is why we suggest a dedicated driver account and why the code is open source, so you can read it or run your own copy.
- Rivolt is in beta and run by ZeroDowntime LLC, a small operator. The [Terms](/terms) cover the details.

Ready? [Sign up](/signup) and connect on the first screen; it takes about two minutes. Questions: [support@rivolt.dev](mailto:support@rivolt.dev).

Rivian is a trademark of Rivian Automotive, LLC. Rivolt is an independent project and is not affiliated with, endorsed by or sponsored by Rivian.
