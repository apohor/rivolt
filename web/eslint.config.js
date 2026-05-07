// ESLint flat config — focused on catching the bug classes that
// actually slip past `tsc -b` and bite users in production.
//
// What's covered (in priority order):
//   1. react-hooks/rules-of-hooks — Rules of Hooks. Catches the
//      "hook after early return" pattern that has previously shipped
//      and produced a blank Drive Details page on hard refresh.
//      Runtime-only failure mode that TypeScript can't detect.
//   2. react-hooks/exhaustive-deps — useEffect/useMemo dep arrays.
//      Warning rather than error so a stale dep doesn't block CI on
//      legitimate cases (function refs that the linter can't prove
//      stable), but it's loud enough to notice.
//   3. typescript-eslint recommended — unused imports, unused vars,
//      no-explicit-any defaults, etc. Plenty of overlap with `tsc`
//      but cheap to keep.
//
// What we deliberately don't run:
//   - eslint-plugin-react (JSX a11y, prop-types) — react-hooks is
//     the high-value subset; the rest is noise on a TS project that
//     uses function components and prop types from interfaces.
//   - prettier integration — no prettier in the repo today; not
//     adding it as part of this round.
//
// Ignored paths: build outputs and the generated PWA icon scripts.

import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import tseslint from "typescript-eslint";

export default tseslint.config(
  {
    ignores: [
      "dist/**",
      "node_modules/**",
      "scripts/**",
      "public/**",
    ],
  },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["src/**/*.{ts,tsx}"],
    languageOptions: {
      ecmaVersion: "latest",
      sourceType: "module",
      globals: {
        ...globals.browser,
      },
    },
    plugins: {
      "react-hooks": reactHooks,
    },
    rules: {
      // Hard error: hooks ordering must be consistent.
      "react-hooks/rules-of-hooks": "error",
      // Loud warning rather than error: stale-closure defects are
      // nice to fix but too noisy on legitimate stable-ref cases to
      // block CI.
      "react-hooks/exhaustive-deps": "warn",
      // We use `_` as a leading sigil for intentional unused
      // parameters in destructured signatures; teach the rule.
      "@typescript-eslint/no-unused-vars": [
        "error",
        {
          argsIgnorePattern: "^_",
          varsIgnorePattern: "^_",
          caughtErrorsIgnorePattern: "^_",
        },
      ],
      // `any` is sometimes the pragmatic choice at API boundaries
      // we don't control (Leaflet runtime types, etc.). Keep it as
      // a warning so it's visible but not blocking.
      "@typescript-eslint/no-explicit-any": "warn",
    },
  },
);
