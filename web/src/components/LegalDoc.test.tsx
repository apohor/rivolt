import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
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

describe("LegalDoc fetching its text", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows a loading line, then the text, once the asset arrives", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, text: async () => fixture }) as unknown as Response));
    render(
      <MemoryRouter>
        <LegalDoc src="/assets/legal-fetch-ok.md" other={{ to: "/privacy", label: "Privacy" }} />
      </MemoryRouter>,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Loading");
    expect(await screen.findByRole("heading", { level: 2, name: "Free and Pro" })).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("says so and offers a retry when the asset cannot be fetched", async () => {
    const fn = vi.fn(async () => ({ ok: false, status: 500, text: async () => "" }) as unknown as Response);
    vi.stubGlobal("fetch", fn);
    render(
      <MemoryRouter>
        <LegalDoc src="/assets/legal-fetch-bad.md" other={{ to: "/privacy", label: "Privacy" }} />
      </MemoryRouter>,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent("could not be loaded");
    fn.mockImplementation(async () => ({ ok: true, status: 200, text: async () => fixture }) as unknown as Response);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { level: 2, name: "Free and Pro" })).toBeInTheDocument();
  });
});
