import fs from "node:fs";
import path from "node:path";
import { expect, test } from "@playwright/test";
import { stubApi } from "./stub";

// With two or more trucks, Now and Truck show a vehicle picker in the page header. On a phone it wraps under the title instead of
// drawing over it, and a long (or Pro-locked) truck name truncates. Nothing in the header (title, picker, period or chip) may
// overlap another part, and everything stays inside the viewport.
const SHOTS = path.join(process.cwd(), "e2e", "__screenshots__");
test.beforeAll(() => fs.mkdirSync(SHOTS, { recursive: true }));

const VEHICLES = [
  { id: "veh-sample-1", vin: "SAMPLE0000000001", name: "Sample R1S Quad Max", model: "R1S", pack_kwh: 135 },
  { id: "veh-sample-2", vin: "SAMPLE0000000002", name: "Second truck with a very long name that goes on and on and on", model: "R1T", pack_kwh: 135, pro_locked: true },
];

const PAGES = [
  { name: "truck", url: "/truck", design: [] as string[] },
  { name: "now", url: "/", design: [] as string[] },
  { name: "now_v2", url: "/", design: ["now_v2"] },
  { name: "now_tidy", url: "/", design: ["now_v2", "now_tidy"] },
];

for (const pg of PAGES) {
  for (const [name, vp] of [
    ["390", { width: 390, height: 844 }],
    ["320", { width: 320, height: 640 }],
    ["1280", { width: 1280, height: 900 }],
  ] as const) {
    for (const scheme of ["light", "dark"] as const) {
      test(`${pg.name} header: vehicle picker clears the title on a phone · ${name} ${scheme}`, async ({ browser }) => {
        const ctx = await browser.newContext({ viewport: vp, colorScheme: scheme });
        const page = await ctx.newPage();
        await stubApi(page, { scenario: "parked", nowEndpoint: true, designPreview: pg.design });
        await page.route("**/api/vehicles", (r) => r.fulfill({ json: VEHICLES }));
        await page.goto(pg.url);
        await expect(page.getByLabel("Vehicle")).toBeVisible();
        const parts = await page.locator("[data-ui-header]").first().evaluate((header) => {
          const rect = (b: DOMRect) => ({ x: b.x, y: b.y, w: b.width, h: b.height });
          const out: Record<string, { x: number; y: number; w: number; h: number }> = {};
          // The h1 box can be squeezed to nothing, so measure where its text is drawn.
          const r = document.createRange();
          r.selectNodeContents(header.querySelector("h1")!);
          out.title = rect(r.getBoundingClientRect());
          const actions = header.querySelector(":scope > div:last-child")!;
          const kids: Element[] = [];
          for (const k of Array.from(actions.children)) {
            if (k.matches("[data-ui-area='scope']")) kids.push(...Array.from(k.children));
            else kids.push(k);
          }
          kids.forEach((k, i) => {
            const b = k.getBoundingClientRect();
            if (b.width > 0 && b.height > 0) out[`action ${i}`] = rect(b);
          });
          return out;
        });
        const names = Object.keys(parts);
        expect(names.length, "title and at least the picker").toBeGreaterThan(1);
        const apart = (a: (typeof parts)[string], b: (typeof parts)[string]) =>
          a.x + a.w <= b.x + 0.5 || b.x + b.w <= a.x + 0.5 || a.y + a.h <= b.y + 0.5 || b.y + b.h <= a.y + 0.5;
        for (let i = 0; i < names.length; i++) {
          expect(parts[names[i]].x + parts[names[i]].w, `${names[i]} inside the viewport`).toBeLessThanOrEqual(vp.width);
          for (let j = i + 1; j < names.length; j++) expect(apart(parts[names[i]], parts[names[j]]), `${names[i]} and ${names[j]} do not overlap`).toBe(true);
        }
        await page.screenshot({ path: path.join(SHOTS, `header-${pg.name}-${name}-${scheme}.png`) });
        await ctx.close();
      });
    }
  }
}
