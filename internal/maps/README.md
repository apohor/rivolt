# internal/maps

The server side of the maps: same-origin proxies and allowlists in front of the self-hosted tile, glyph, routing and terrain services, so a
map never asks a third party about a person's drives.

## Terrain (hillshade) and its credit

`dem.go` serves Terrarium terrain tiles from the in-cluster elevation mirror. The web map draws a hillshade from them (`web/src/map/style.ts`).
The terrain dataset requires its sources to be credited **wherever the hillshade is drawn**:

- Web: `TERRAIN_ATTRIBUTION` in `web/src/map/style.ts` is set on the `dem` source, so MapLibre shows it with the basemap credit. Do not
  remove it while the hillshade layer exists (`style.test.ts` checks it).
- iPhone: the iPhone map draws no hillshade today. If it ever does, the same credit must ship in the same change, in the map's credit line
  (`MapAttribution`, `ios/Rivolt/Map/DriveRouteMap.swift`) and in Acknowledgements, before that build goes out. The OpenStreetMap and
  Protomaps credits are already there; the terrain one is separate and is not covered by them.
- Elevation used for numbers (drive altitude, the Elevation colour mode) comes from the same dataset but draws no layer; its disclosure is the
  Privacy page's line about the self-hosted Terrarium copy.

## Radar (live weather) and its credit

`internal/radar` serves NOAA/NWS MRMS base reflectivity tiles at `/api/maps/radar/{z}/{x}/{y}.png` (and the frame time at
`/api/maps/radar/frame`), behind `RIVOLT_RADAR=1` (off by default; `RIVOLT_RADAR_UPSTREAM` overrides the GeoServer). NWS content is public
domain, free for any lawful purpose provided we do not claim it as our own, imply an endorsement or affiliation, or modify it and present it as
official. So wherever radar is drawn the map credit says "Radar: NOAA/NWS" and the credits page says Rivolt is not endorsed by or affiliated with
NOAA/NWS; the picture is NOAA's own colouring (we reproject, we do not recolour). Clients never call NOAA: this server does, with no user, viewport
or account in the request, one fetch per tile per frame across replicas.

### Forecast radar for the planner map (Pro)

With `RIVOLT_RADAR_BASE_URL` set (the in-cluster radar service the trip planner's leg tags read), `GET /api/maps/forecast-radar/hours` lists the hours
of the newest usable cycle and `GET /api/maps/forecast-radar/{cycle}/{fh}/{z}/{x}/{y}.png` (z 3 to 7) draws one hour as a tile, in the radar legend's
colours, from the same grids. No new job and no storage: a tile is rendered on request and cached by address (`private, max-age=86400, immutable`;
16 MB of PNGs per pod). Pro accounts only (402 `pro_required` otherwise). Where the forecast has no value (outside the grid, no data) the tile is
transparent, never a "dry" colour. Credit: NOAA RRFS. Like the live tiles, the address is logged and traced as the route only.
