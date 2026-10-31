# Map label glyphs (self-hosted)

Signed-distance-field glyph ranges (Mapbox/MapLibre `.pbf`) for the web map's labels, embedded in the server binary and served at
`/api/maps/glyphs/{fontstack}/{range}.pbf` (`internal/maps/glyphs.go`), so the map makes no request to any other origin for labels.

- Fonts: Noto Sans Regular, Noto Sans Medium, Noto Sans Italic. Ranges: 0-255 and 256-511 (Basic Latin, Latin-1, Latin Extended-A and B),
  enough for US and Canadian place names with their accents. Other scripts have no glyphs here: a label in them draws without those characters.
- Source: <https://github.com/protomaps/basemaps-assets>, directory `fonts/`, at commit `028c18f713baecad011301ff7a69acc39bcc2ae7`
  (2025-10-31), files copied unchanged. The SHA-256 of each is pinned in `glyphs_test.go`, so a changed or replaced file fails CI.
- Licence: SIL Open Font License 1.1 (`OFL.txt`, copied from the same commit), Copyright 2022 The Noto Project Authors. The fonts are
  redistributed unchanged and are not sold on their own.
- There is no sprite: the map style uses no icons, and no sprite route exists.

To add a font or a range: copy the files from the same repository at a pinned commit, update this file and the pinned table in the test.
