import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { stubLegalFetch } from "../test/legalFetch";
import { TERMS_VERSIONS } from "../lib/termsVersions";
import TermsPage from "./TermsPage";
import TermsArchivePage from "./TermsArchivePage";

describe("earlier versions of the Terms", () => {
  beforeEach(() => stubLegalFetch());
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("/terms links every archived version", async () => {
    render(<MemoryRouter initialEntries={["/terms"]}><TermsPage /></MemoryRouter>);
    await screen.findByRole("heading", { level: 1, name: "Rivolt Terms of Service" });
    for (const v of TERMS_VERSIONS) {
      expect(screen.getByRole("link", { name: v.label })).toHaveAttribute("href", `/terms/${v.key}`);
    }
    expect(TERMS_VERSIONS.map((v) => v.key)).toEqual(["2026-11-21", "2026-11-17", "2026-10-23"]);
  });

  it("each archive page links back to the current Terms", async () => {
    for (const v of TERMS_VERSIONS) {
      const { unmount } = render(<MemoryRouter initialEntries={[`/terms/${v.key}`]}><TermsArchivePage version={v.key} /></MemoryRouter>);
      await screen.findByRole("heading", { level: 1, name: "Rivolt Terms of Service" });
      expect(screen.getByText(new RegExp(`dated ${v.label}`))).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Current Terms" })).toHaveAttribute("href", "/terms");
      unmount();
    }
  });
});
