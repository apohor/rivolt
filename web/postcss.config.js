export default {
  plugins: {
    tailwindcss: {},
    // Downlevel modern CSS (nesting, :has(), custom-media, logical props, etc.)
    // to syntax supported by the browserslist targets in package.json.
    // stage 2 = reasonably stable proposals.
    // Tailwind handles its own nesting, so disable the nesting-rules plugin here
    // to avoid double-processing.
    'postcss-preset-env': {
      stage: 2,
      features: {
        'nesting-rules': false,
      },
    },
    autoprefixer: {},
  },
};
