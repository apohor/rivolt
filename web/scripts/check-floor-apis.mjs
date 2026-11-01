// Fails when the shipped JS uses a platform API newer than the supported browsers, now that the build ships no polyfills.
//   node scripts/check-floor-apis.mjs [distDir]
// The floor is vite.config.ts's modernTargets (Safari/iOS 16.4, Chrome/Edge 111, Firefox 128). Each entry is an API one of those lacks;
// the build used to inject core-js for all of them (a 20.8 KB chunk) whether or not any code called them. A dependency bump that starts
// calling one fails here, and the fix is to polyfill that one feature (modernPolyfills: ["es.xxx"]) or not take the bump.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

// Object.values/keys/entries return arrays, so their .map is old; a Map, Set or array iterator (or matchAll) is what the helpers are new on.
const iter = "(?:(?<!\\bObject)\\.(?:values|keys|entries)|\\.matchAll)\\([^()]*\\)";
const helpers = "(?:map|filter|forEach|some|every|find|reduce|flatMap|take|drop|toArray)";

export const FLOOR_APIS = [
  ["Object.groupBy / Map.groupBy", /\b(?:Object|Map)\.groupBy\b/, "Safari 17.4, Chrome 117"],
  ["Promise.withResolvers", /\bPromise\.withResolvers\b/, "Safari 17.4, Chrome 119"],
  ["URL.canParse", /\bURL\.canParse\b/, "Safari 17, Chrome 120"],
  ["Array.fromAsync", /\bArray\.fromAsync\b/, "Chrome 121"],
  ["ArrayBuffer.prototype.transfer", /\.transfer(?:ToFixedLength)?\(/, "Safari 17.4, Chrome 114"],
  ["Set methods", /\.(?:union|intersection|difference|symmetricDifference|isSubsetOf|isSupersetOf|isDisjointFrom)\(/, "Safari 17, Chrome 122"],
  ["Iterator helpers", new RegExp(`\\bIterator\\.(?:from|concat)\\b|${iter}\\.${helpers}\\(`), "Safari 18.4, Chrome 122, Firefox 131"],
  ["Map/WeakMap getOrInsert", /\.getOrInsert(?:Computed)?\(/, "not yet shipped"],
  ["Promise.try", /\bPromise\.try\b/, "Safari 18.2, Chrome 128, Firefox 134"],
  ["RegExp.escape / Error.isError", /\bRegExp\.escape\b|\bError\.isError\b/, "Safari 18.2, Chrome 136, Firefox 134"],
  ["Uint8Array base64 / hex", /\.(?:toBase64|toHex|setFromBase64|setFromHex)\(|\bUint8Array\.from(?:Base64|Hex)\b/, "Safari 18.2, Chrome 140, Firefox 133"],
  ["JSON.parse source (rawJSON)", /\bJSON\.(?:rawJSON|isRawJSON)\b/, "Safari 18.4, Chrome 114"],
  ["Float16Array / Math.sumPrecise", /\bFloat16Array\b|\bMath\.(?:f16round|sumPrecise)\b/, "Safari 18.2, Chrome 135"],
  ["explicit resource management", /\bDisposableStack\b|\bSymbol\.(?:dispose|asyncDispose)\b/, "Chrome 134, Firefox 141"],
  ["Temporal", /\bTemporal\.(?:Now|PlainDate|PlainDateTime|ZonedDateTime|Instant|Duration)\b/, "Firefox 139, Chrome 144"],
  ["Intl.DurationFormat", /\bIntl\.DurationFormat\b/, "Safari 18.4, Chrome 129"],
];

export function findFloorApis(text) {
  return FLOOR_APIS.filter(([, re]) => re.test(text)).map(([name]) => name);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const dir = join(process.argv[2] ?? "../internal/web/dist", "assets");
  let bad = 0;
  let files = 0;
  for (const f of readdirSync(dir)) {
    if (!/\.m?js$/.test(f)) continue;
    files++;
    const found = findFloorApis(readFileSync(join(dir, f), "utf8"));
    if (found.length) {
      bad++;
      console.error(`${f}: uses ${found.join(", ")}`);
    }
  }
  if (files === 0) {
    console.error(`no JS assets in ${dir}`);
    process.exit(1);
  }
  if (bad) {
    console.error("the build ships no polyfills: polyfill the feature (modernPolyfills: [...]) or avoid the API");
    process.exit(1);
  }
  console.log(`${files} JS assets use nothing newer than the supported browsers`);
}
