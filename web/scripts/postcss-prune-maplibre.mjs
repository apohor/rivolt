// Drops the rules of MapLibre controls Rivolt never creates from maplibre-gl.css, at build time (Maps 10). The stylesheet is 70 kB raw and
// more than half of it is the MapLibre logo (an inline SVG, also repeated in the forced-colors blocks) and the icons of the geolocate,
// fullscreen, terrain, globe, scale and compass controls. Rivolt's maps use the zoom buttons, the compact attribution, popups and markers.
// Only maplibre-gl's own stylesheet is touched; a selector list loses just its unused members, and an emptied @media block goes with them.
export const UNUSED_CONTROLS = /\.maplibregl-(?:ctrl-logo|ctrl-geolocate|ctrl-fullscreen|ctrl-shrink|ctrl-terrain|ctrl-globe|ctrl-scale|ctrl-compass|user-location)/;

// Never pruned, whatever UNUSED_CONTROLS says: the attribution control (the OpenStreetMap and Protomaps credits must stay visible, and
// foldCredits/map.css depend on its structure) and the compact class it toggles. Only the logo's own compact rule is not protected.
export const KEEP = (selector) => /\.maplibregl-ctrl-attrib/.test(selector) || (/\.maplibregl-compact/.test(selector) && !/ctrl-logo/.test(selector));

const plugin = () => ({
  postcssPlugin: "rivolt-prune-maplibre",
  Once(root) {
    const file = root.source?.input?.file ?? "";
    if (!/[\\/]maplibre-gl[^\\/]*\.css$/.test(file)) return;
    root.walkRules((rule) => {
      const keep = rule.selectors.filter((s) => KEEP(s) || !UNUSED_CONTROLS.test(s));
      if (keep.length === 0) rule.remove();
      else if (keep.length !== rule.selectors.length) rule.selectors = keep;
    });
    // An at-rule (a @media block) that has nothing left in it goes too.
    root.walkAtRules((at) => {
      if (at.nodes && at.nodes.length === 0 && /^(media|supports|layer)$/i.test(at.name)) at.remove();
    });
  },
});
plugin.postcss = true;
export default plugin;
