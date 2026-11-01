import { describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import guide from "../content/r2.md?raw";
import { R2CheckedContent } from "../components/R2Checked";
import R2GuidePage from "./R2GuidePage";

// The public guide and the Truck card say the same thing about the R2, item for item (DA's condition on publishing it).
describe("the R2 guide matches the Truck card's checked list", () => {
  const card = () => {
    const { container } = render(
      <MemoryRouter>
        <R2CheckedContent />
      </MemoryRouter>,
    );
    const blocks = Array.from(container.querySelectorAll("div > h4")).map((h) => [h.textContent ?? "", h.nextElementSibling?.textContent ?? ""] as const);
    return Object.fromEntries(blocks);
  };
  const items = (s: string) => s.split("·").map((x) => x.replace(/\s+/g, " ").trim().replace(/\.$/, "")).filter(Boolean);
  const text = guide.toLowerCase().replace(/\s+/g, " ");

  it("lists every checked item the card lists", () => {
    const checked = items(card()["Checked on real R2s"]);
    expect(checked.length).toBeGreaterThanOrEqual(9);
    for (const item of checked) expect(text, item).toContain(item.toLowerCase());
  });

  it("lists every item the card says is not on the R2 yet", () => {
    const not = items(card()["Not on the R2 yet"].replace(/\bEach appears.*$/, ""));
    expect(not.length).toBeGreaterThanOrEqual(5);
    for (const item of not) expect(text, item).toContain(item.toLowerCase());
  });

  it("calls the countdown an estimate and says it is unofficial", () => {
    expect(text).toContain("estimate");
    expect(text).toContain("unofficial and not affiliated with rivian");
  });

  it("has the five-step pre-delivery checklist before Day 1, and does not promise emails that wait", () => {
    const head = guide.indexOf("## Before your R2 arrives");
    expect(head).toBeGreaterThan(-1);
    expect(head).toBeLessThan(guide.indexOf("## Day 1: connect"));
    const section = guide.slice(head, guide.indexOf("## Day 1: connect"));
    expect(section.match(/^\d\. \*\*/gm)).toHaveLength(5);
    expect(section.toLowerCase()).not.toContain("will wait");
    expect(section.toLowerCase()).not.toContain("within minutes");
  });
});

describe("the R2 guide's checklist", () => {
  it("shows its numbers: an ordered list styled as decimal, five steps", () => {
    const { container } = render(
      <MemoryRouter>
        <R2GuidePage />
      </MemoryRouter>,
    );
    const ol = container.querySelector("ol");
    expect(ol).not.toBeNull();
    expect(ol!.className).toContain("list-decimal");
    expect(ol!.querySelectorAll(":scope > li").length).toBe(5);
  });
});
