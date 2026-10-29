# Is it safe to connect my Rivian account to Rivolt?

Rivolt asks for your Rivian sign-in so it can read your truck's drives and charging. That is a real decision, so here is what Rivolt does with it, in plain terms, and what the risks are. Rivolt is a free, open-source companion for Rivian owners. It is unofficial and not affiliated with, endorsed by or sponsored by Rivian.

## The safest way: a separate Authorized Driver account

You don't have to give Rivolt your main login. We recommend connecting a second, free Rivian account that you add as an Authorized Driver on your vehicle.

- Rivolt then holds that account's session, not yours.
- To remove Rivolt, you remove the driver in the Rivian app. You don't need to change your main password.
- Rivolt's requests count against the dedicated account, not your main one.

Trade-offs: you need a second email address and a few minutes in the Rivian app, and an Authorized Driver account may not see all of the history your main account holds, such as charges the owner started. Live data from the moment you connect is complete. You can still connect your main account if you prefer. The [step-by-step guide](https://github.com/apohor/rivolt/blob/main/docs/SIGNUP.md#recommended-connect-a-dedicated-authorized-driver-account) walks through it.

## What Rivolt keeps

- Your Rivian email and an encrypted session token.
- Your Rivian password is never stored. It is used once to sign in to Rivian.
- The drives and charges it records for your truck, including location. The [Privacy policy](/privacy) says what is recorded, how long it is kept and how to delete it.

## What Rivolt does

- It is read-only. It reads telemetry, drives and charging, and it can't control your truck. Rivolt has never sent a command to your vehicle. The only write-style requests it makes to Rivian are the sign-in steps.
- It does not use third-party analytics.

## How to undo it

- In Rivolt, go to Settings → Account → Rivian account and sign out of Rivian. The stored session is deleted.
- Or remove the Authorized Driver in the Rivian app.
- You can export your data as a zip of CSVs (charges with cost source, drives and trips) or download a full JSON backup, and delete it from Settings.

## The honest risks

- Rivolt is unofficial. It uses Rivian's unofficial API, so it can break, and Rivian can change or block access at any time, with either kind of account.
- Any service that holds a session token carries some risk. That is why we suggest a dedicated driver account and why the code is open source, so you can read it or run your own copy.
- Rivolt is in beta and run by ZeroDowntime LLC, a small operator. The [Terms](/terms) cover the details.

Want to look first? The [demo](https://demo.rivolt.dev) uses a generated sample truck, with no sign-in and no Rivian account.

Rivian is a trademark of Rivian Automotive, LLC. Rivolt is an independent project and is not affiliated with, endorsed by or sponsored by Rivian.
