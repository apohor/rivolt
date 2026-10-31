import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import TermsArchivePage from "./TermsArchivePage";

describe("/terms/2026-10-23", () => {
  afterEach(() => cleanup());

  it("shows the October 23 Terms with a note that says it does not change and points to the current ones", () => {
    const { container } = render(
      <MemoryRouter initialEntries={["/terms/2026-10-23"]}>
        <TermsArchivePage />
      </MemoryRouter>,
    );
    expect(screen.getByRole("heading", { level: 1, name: "Rivolt Terms of Service" })).toBeInTheDocument();
    expect(screen.getByText(/Last updated: October 23, 2026/)).toBeInTheDocument();
    const note = container.querySelector("[data-terms-archive]");
    expect(note?.textContent).toMatch(/dated October 23, 2026.*It does not change/);
    expect(note?.querySelector("a")).toHaveAttribute("href", "/terms");
    expect(screen.getByRole("link", { name: "Current Terms" })).toHaveAttribute("href", "/terms");
  });
});
