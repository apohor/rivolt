import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import App from "../App";
import { stubLegalFetch } from "../test/legalFetch";
import { TERMS_VERSIONS } from "../lib/termsVersions";
import TermsPage from "./TermsPage";

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

  // Through the app's own router: a version in the list without a route in App.tsx would be a dead link and fail here.
  it("every listed version opens from /terms in the app, with its date and the link back", async () => {
    for (const v of TERMS_VERSIONS) {
      const { unmount } = render(
        <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
          <MemoryRouter initialEntries={["/terms"]}><App /></MemoryRouter>
        </QueryClientProvider>,
      );
      await screen.findByRole("heading", { level: 1, name: "Rivolt Terms of Service" });
      fireEvent.click(screen.getByRole("link", { name: v.label }));
      expect(await screen.findByText(new RegExp(`dated ${v.label}`))).toBeInTheDocument();
      expect(await screen.findByText(new RegExp(`Last updated: ${v.label}`))).toBeInTheDocument();
      fireEvent.click(screen.getByRole("link", { name: "Current Terms" }));
      expect(await screen.findByRole("link", { name: v.label })).toBeInTheDocument();
      unmount();
    }
  });
});
