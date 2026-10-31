// What every MapLibre map in the app shares: the style for a flavour from what the server advertises.
import { tilesDEM, tilesGlyphsURL, tilesPMTilesURL } from "../lib/config";
import { buildStyle, plainStyle, type MapFlavor } from "./style";

// styleFor is the style for a flavour from what the server advertises: the full basemap when it advertises a tile archive, a plain background
// when it does not.
export function styleFor(flavor: MapFlavor) {
  const tiles = tilesPMTilesURL();
  if (!tiles.startsWith("/")) return plainStyle(flavor);
  const dem = tilesDEM();
  return buildStyle({ flavor, origin: location.origin, tilesPath: tiles, glyphsPath: tilesGlyphsURL(), dem: dem.url ? dem : null });
}
