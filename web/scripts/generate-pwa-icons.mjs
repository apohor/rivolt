// Generates PWA icons from public/favicon.svg using @resvg/resvg-js.
// Run from web/: `node scripts/generate-pwa-icons.mjs`.
//
// Outputs to public/:
//   - icon-192.png           (192x192, any)
//   - icon-512.png           (512x512, any)
//   - icon-maskable-512.png  (512x512, maskable — safe zone within 80%)
//   - apple-touch-icon.png   (180x180, iOS home-screen)
import { readFileSync, writeFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { Resvg } from "@resvg/resvg-js";

const here = dirname(fileURLToPath(import.meta.url));
const publicDir = resolve(here, "..", "public");
const src = readFileSync(resolve(publicDir, "favicon.svg"), "utf8");

function renderAt(svg, size, outfile) {
  const png = new Resvg(svg, {
    fitTo: { mode: "width", value: size },
    background: "#0a0a0a",
  })
    .render()
    .asPng();
  writeFileSync(resolve(publicDir, outfile), png);
  console.log(`✔ ${outfile} (${size}×${size}, ${png.length} bytes)`);
}

// "Any" purpose variants — the favicon already has its own rounded corners
// at the edge; at 192/512 the corner radius scales naturally, so no extra
// padding is needed here.
renderAt(src, 180, "apple-touch-icon.png");
renderAt(src, 192, "icon-192.png");
renderAt(src, 512, "icon-512.png");

// Maskable: the OS may mask the icon into a circle/squircle/rounded-square
// of its choosing; safe-zone spec says content must fit inside a central
// disc of diameter = 80% of the image. Wrap the original favicon (viewBox
// 0 0 64 64) inside an 80×80 canvas so the drawing occupies the inner 80%.
const maskable = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 80 80">
  <rect width="80" height="80" fill="#0a0a0a"/>
  <g transform="translate(8,8)">${src.replace(/^[\s\S]*?<svg[^>]*>/, "").replace(/<\/svg>\s*$/, "")}</g>
</svg>`;
renderAt(maskable, 512, "icon-maskable-512.png");
