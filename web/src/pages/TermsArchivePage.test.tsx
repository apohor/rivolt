import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { stubLegalFetch } from "../test/legalFetch";
import TermsArchivePage from "./TermsArchivePage";

describe("/terms/2026-10-23", () => {
  beforeEach(() => stubLegalFetch());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows the October 23 Terms with a note that says it does not change and points to the current ones", async () => {
    const { container } = render(
      <MemoryRouter initialEntries={["/terms/2026-10-23"]}>
        <TermsArchivePage />
      </MemoryRouter>,
    );
    expect(await screen.findByRole("heading", { level: 1, name: "Rivolt Terms of Service" })).toBeInTheDocument();
    expect(screen.getByText(/Last updated: October 23, 2026/)).toBeInTheDocument();
    const note = container.querySelector("[data-terms-archive]");
    expect(note?.textContent).toMatch(/dated October 23, 2026.*It does not change/);
    expect(note?.querySelector("a")).toHaveAttribute("href", "/terms");
    expect(screen.getByRole("link", { name: "Current Terms" })).toHaveAttribute("href", "/terms");
  });
});
