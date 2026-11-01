// Fails when the shipped JS uses a platform API newer than the supported browsers, now that the build ships no polyfills.
//   node scripts/check-floor-apis.mjs [distDir]
// The floor is vite.config.ts's modernTargets (Safari/iOS 16.4, Chrome/Edge 111, Firefox 128). Each entry is an API one of those lacks;
// the build used to inject core-js for all of them (a 20.8 KB chunk) whether or not any code called them. A dependency bump that starts
// calling one fails here, and the fix is to polyfill that one feature (modernPolyfills: ["es.xxx"]) or not take the bump.
// Known limit: the scan is textual, on minified output. It catches named globals and distinctive method names, not a generator's iterator
// `.map` or a computed property access, so it is a tripwire, not a proof of every call.
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
  ["URL.parse", /\bURL\.parse\b/, "Safari 18, Chrome 126, Firefox 126"],
  ["Promise.try", /\bPromise\.try\b/, "Safari 18.2, Chrome 128, Firefox 134"],
  ["RegExp.escape / Error.isError", /\bRegExp\.escape\b|\bError\.isError\b/, "Safari 18.2, Chrome 136, Firefox 134"],
  ["Uint8Array base64 / hex", /\.(?:toBase64|toHex|setFromBase64|setFromHex)\(|\bUint8Array\.from(?:Base64|Hex)\b/, "Safari 18.2, Chrome 140, Firefox 133"],
  ["JSON.parse source (rawJSON)", /\bJSON\.(?:rawJSON|isRawJSON)\b/, "Safari 18.4, Chrome 114"],
  ["Float16Array / Math.sumPrecise", /\bFloat16Array\b|\bMath\.(?:f16round|sumPrecise)\b/, "Safari 18.2, Chrome 135"],
  ["explicit resource management", /\bDisposableStack\b|\bSymbol\.(?:dispose|asyncDispose)\b/, "Chrome 134, Firefox 141"],
  ["Temporal", /\bTemporal\.(?:Now|PlainDate|PlainDateTime|ZonedDateTime|Instant|Duration)\b/, "Firefox 139, Chrome 144"],
  ["Intl.DurationFormat", /\bIntl\.DurationFormat\b/, "Safari 18.4, Chrome 129"],
];

// Method-name entries are ambiguous: a library's own class method of the same name is not the new built-in. Each exemption is named, a chunk
// file prefix and the method, with the reason, so a new library that defines and calls such a name still trips the scan for a human look.
export const ALLOWED = [
  { prefix: "maplibre-gl-", method: "difference", why: "the render-to-texture fingerprint class has its own difference(e){...}, not Set.prototype.difference" },
];
const METHOD_ENTRIES = ["Set methods", "Map/WeakMap getOrInsert", "ArrayBuffer.prototype.transfer"];
const allowed = (file, method) => ALLOWED.some((a) => file.startsWith(a.prefix) && a.method === method);

export function findFloorApis(text, file = "") {
  return FLOOR_APIS.filter(([name, re]) => {
    if (!METHOD_ENTRIES.includes(name)) return re.test(text);
    for (const m of text.matchAll(new RegExp(re.source, "g"))) {
      const method = /([A-Za-z]+)\($/.exec(m[0])?.[1];
      if (!method || !allowed(file, method)) return true;
    }
    return false;
  }).map(([name]) => name);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const dir = join(process.argv[2] ?? "../internal/web/dist", "assets");
  let bad = 0;
  let files = 0;
  for (const f of readdirSync(dir)) {
    if (!/\.m?js$/.test(f)) continue;
    files++;
    const found = findFloorApis(readFileSync(join(dir, f), "utf8"), f);
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
