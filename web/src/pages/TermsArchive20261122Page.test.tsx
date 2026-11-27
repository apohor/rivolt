import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { stubLegalFetch } from "../test/legalFetch";
import TermsArchivePage from "./TermsArchivePage";

describe("/terms/2026-11-22", () => {
  beforeEach(() => stubLegalFetch());
  afterEach(() => vi.unstubAllGlobals());

  it("shows the November 21 Terms with the archive note and a link to the current Terms", async () => {
    const { container } = render(
      <MemoryRouter initialEntries={["/terms/2026-11-22"]}>
        <TermsArchivePage version="2026-11-22" />
      </MemoryRouter>,
    );
    expect(await screen.findByText(/Last updated: November 22, 2026/)).toBeInTheDocument();
    expect(container.querySelector("[data-terms-archive]")?.textContent).toMatch(/dated November 22, 2026.*It does not change/);
    expect(screen.getByRole("link", { name: "Current Terms" })).toHaveAttribute("href", "/terms");
  });
});
