# Recreation.gov (RIDB) campground data

Federal campgrounds with electric hookups feed the overnight picker next to the
NREL charger listing (`kind: "campground"`). This note is the record of where the
data comes from and what we may do with it. Re-verify the licence line below
whenever the source changes, and date the check.

## Source

- The RIDB (Recreation Information Database) public bulk export:
  `https://ridb.recreation.gov/downloads/RIDBFullExport_V1_CSV.zip`
  (about 250 MB, no API key, rebuilt daily; the 2026-10-20 build was read for
  this note). The monthly archive build downloads it once. There are no
  per-user calls and no user data goes to RIDB. The API key (needed only for
  the REST API, not for this file) is not used.
- Tables used: `Facilities` (id, name, lat/lon, `Reservable`, `FacilityUseFeeDescription`,
  `LastUpdatedDate`), `Campsites` and `CampsiteAttributes` (`Electricity Hookup`).
- Stored per campground, facts only: RIDB FacilityID, name, lat/lon, highest
  electric hookup in amps, number of campsites with power and in total, whether
  it is reservable, the fee text, the export date. Nothing else from the export
  (no descriptions, photos, reviews, emails, phone numbers or other personal
  fields) is copied.
- Hookup values are text such as `50`, `30`, `20/30/50`, `15`, `N/A`; the highest
  number in the text is the campground's `hookup_amps`. 50 A is a NEMA 14-50
  (240 V), 30 A is a TT-30 (120 V), 15-20 A is a 120 V outlet.

## Licence (checked 2026-10-20)

- data.gov's record for the RIDB dataset states
  `"license": "https://creativecommons.org/licenses/by/4.0/"` (CC BY 4.0). The
  record itself was last modified 2022-08-25 and gives no attribution wording.
- RIDB's own documentation page is a JavaScript page; no terms text could be
  read from it without a browser. **Open item:** someone should read the
  Recreation.gov / RIDB terms in a browser and add the result here.
- Attribution we show: **"Data: Recreation.gov (RIDB), CC BY 4.0"**, in the
  overnight response (`attribution`) and on every campground card. The only
  outbound link is `https://www.recreation.gov/camping/campgrounds/<numeric FacilityID>`.

## Rules

- Official export only; never scraping the site.
- One bulk pull a month, in the chargers archive build job. A short or empty
  export never replaces the archive (the job aborts under 500 campgrounds with
  electric sites), and the server's 80% shrink guard still applies.
- Power is planned, not listed: RIDB gives amps, not kW. Through Rivian's
  Portable Charger (32 A, 7.6 kW at most) a 50 A site is planned at 7.2 kW; a
  30 A TT-30 site needs an adapter the charger does not ship (1.4 kW at 120 V);
  15-20 A sites are not offered. Reservations are the campground's: the card says
  "listed, not guaranteed".
