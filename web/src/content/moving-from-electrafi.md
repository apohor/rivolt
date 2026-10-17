# Moving your history from ElectraFi to Rivolt

If you've tracked your Rivian with ElectraFi, you don't have to start over. Rivolt can import your ElectraFi drives and charges and keep them alongside what it records going forward.

Rivolt is a free, open-source companion for Rivian owners. It is unofficial and not affiliated with Rivian or ElectraFi. This page isn't about which app is better. It is the steps for moving your data if you decide to try Rivolt.

## Before you start

- **Try it first, with no account.** The [demo](https://demo.rivolt.dev) uses a generated sample truck.
- **Sign up** at [rivolt.dev/signup](/signup). Continue with Google or GitHub; it is immediate while there is room, because the beta has an account cap.
- **Connect your truck.** We recommend an Authorized Driver account, so Rivolt never holds your primary login and you can revoke it from the Rivian app at any time.
- **Set your timezone.** Settings → Vehicle → Display → Timezone must match the timezone the ElectraFi CSVs were exported under, or session IDs won't line up.

## Import steps

- In the ElectraFi app, go to **Settings → Account → Advanced → Download ElectraFi Data**. You get a set of CSV files: one `drives.csv` and one `charges.csv` per month.
- In Rivolt, go to **Settings → Data → Import ElectraFi CSV**.
- Select all the CSVs at once and upload. Rivolt reads each file, skips anything it has already recorded live, and merges timestamps so imported history doesn't collide with live data.

## After importing

- Your charges are priced at your home rate. Set it under Settings → Charging → Home charging cost. Rivolt records the rate as of the moment a session ends, so changing it later doesn't rewrite history. Charges recorded before you set a rate stay unpriced; setting a rate doesn't change them, but you can add a price to each one.
- Activity shows a day-grouped feed of drives and charges, and Money shows the monthly ledger.
- Your data stays yours: you can export everything as a JSON backup and delete your data from Settings.

## What to expect

- ABRP and TezLab imports aren't available yet.
- Rivolt is in beta and uses Rivian's unofficial API, so it can break.
- Questions or problems: open an issue on [GitHub](https://github.com/apohor/rivolt), or see the [Privacy](/privacy) and [Terms](/terms) pages.

Rivian is a trademark of Rivian Automotive, LLC. ElectraFi is a product of its own makers. Rivolt is an independent project and is not affiliated with, endorsed by or sponsored by either.
