import { render } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { LegalDoc } from "./LegalDoc";

// The anchors other surfaces link to must exist on the rendered page: the launch notice's "What changes" button goes to
// /support#free-and-pro, and /terms#N deep links are older than the slug rule.
const fixture = ["# Support", "", "## Common questions", "", "text", "", "## Free and Pro", "", "text", "", "## More questions", "", "text", "", "## 11. Plans", "", "text"].join("\n");

describe("LegalDoc heading ids", () => {
  it("renders slug ids for plain headings and the bare number for numbered ones", () => {
    const { container } = render(
      <MemoryRouter>
        <LegalDoc source={fixture} other={{ to: "/privacy", label: "Privacy" }} />
      </MemoryRouter>,
    );
    const ids = [...container.querySelectorAll("h2")].map((h) => h.id);
    expect(ids).toEqual(["common-questions", "free-and-pro", "more-questions", "11"]);
    expect(container.querySelector("#free-and-pro")?.textContent).toBe("Free and Pro");
  });
});
