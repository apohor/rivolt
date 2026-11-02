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
