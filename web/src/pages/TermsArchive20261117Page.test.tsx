import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";
import TermsArchive20261117Page from "./TermsArchive20261117Page";

describe("/terms/2026-11-17", () => {
  it("shows the launch-day Terms with the archive note and a link to the current Terms", () => {
    const { container } = render(
      <MemoryRouter initialEntries={["/terms/2026-11-17"]}>
        <TermsArchive20261117Page />
      </MemoryRouter>,
    );
    expect(screen.getByText(/Last updated: November 17, 2026/)).toBeInTheDocument();
    expect(container.querySelector("[data-terms-archive]")?.textContent).toMatch(/dated November 17, 2026.*It does not change/);
    expect(screen.getByRole("link", { name: "Current Terms" })).toHaveAttribute("href", "/terms");
  });
});
