import tailwindcss from '@tailwindcss/postcss';
import presetEnv from 'postcss-preset-env';
import pruneMapLibre from './scripts/postcss-prune-maplibre.mjs';

export default {
  plugins: [
    // Drops the MapLibre controls Rivolt never creates from maplibre-gl.css (a no-op for every other stylesheet).
    pruneMapLibre(),
    tailwindcss(),
    // Downlevel modern CSS (nesting, :has(), custom-media, logical props, etc.)
    // to syntax supported by the browserslist targets in package.json.
    // stage 2 = reasonably stable proposals.
    // Tailwind handles its own nesting, so disable the nesting-rules plugin here
    // to avoid double-processing.
    presetEnv({
      stage: 2,
      features: {
        'nesting-rules': false,
      },
    }),
  ],
};
