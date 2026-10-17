# Rivolt roadmap

Rivolt is open source and in beta at [rivolt.dev](https://rivolt.dev), with a
read-only demo at [demo.rivolt.dev](https://demo.rivolt.dev). This is the
public view of where it's going; plans change, and there are no dates.

## Shipped
- **Five tabs:** Now, Activity (Feed, Table and Map), Money, Plan and Truck,
  on phone and desktop, in dark and light.
- **Money:** a monthly charging statement priced at your rates, with the
  source of every number, and (where available, opt-in) what Rivian billed
  for a session.
- **Plan:** a map-first trip planner with charging stops, trip cost and Plan
  vs actual for saved trips.
- **Truck:** pack health, parked drain and sleep, tire pressure and the
  installed software version.
- **R2 support:** drives, charges, sleep and plug state.
- **Open signup**, a JSON export of your data, push notifications, and
  self-hosting with docker compose or Helm.

## Now
- Efficiency insights: miles per kWh by temperature, speed and drive mode.
- Explaining "plugged in but not charging" (scheduled charging windows).
- Time to full while charging, and a clearer software-update card on Truck.

## Next
- Smart-charging and cold-weather explanations on Now and Truck.
- A CSV statement export.
- More precise charging cost for public networks.

## Later
- The iOS app beyond its current scaffold (see [`../ios/README.md`](../ios/README.md)).
- Home energy: solar and home-battery context for charging cost.

Ideas and bug reports are welcome as GitHub issues; see
[`../CONTRIBUTING.md`](../CONTRIBUTING.md).
