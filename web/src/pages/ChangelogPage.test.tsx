import { cleanup, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { backend } from "../lib/api";
import ChangelogPage from "./ChangelogPage";

vi.mock("../lib/api", () => ({ backend: { changelog: vi.fn() } }));
vi.mock("../lib/theme", () => ({ useSystemTheme: () => {} }));

const changelog = backend.changelog as unknown as ReturnType<typeof vi.fn>;

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={["/changelog"]}>
        <ChangelogPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const releases = [
  {
    tag: "v0.36.21",
    published_at: "2026-10-18T20:56:28Z",
    title: "v0.36.21",
    body_md: "## Fixes\n\n- **Admin:** clicking a user opens it again.\n\n<img src=x onerror=alert(1)><script>alert(2)</script>",
    url: "https://github.com/apohor/rivolt/releases/tag/v0.36.21",
  },
  { tag: "v0.36.20", published_at: "2026-10-18T20:29:34Z", title: "v0.36.20", body_md: "## What's new\n\n- Clearer emails.", url: "" },
];

describe("ChangelogPage", () => {
  afterEach(() => {
    cleanup();
    changelog.mockReset();
  });

  it("shows each release as a section, newest first, anchored at its tag", async () => {
    changelog.mockResolvedValue({ releases });
    const { container } = renderPage();
    await screen.findByRole("heading", { level: 2, name: /v0\.36\.21/ });
    const sections = container.querySelectorAll("section");
    expect([...sections].map((s) => s.id)).toEqual(["v0.36.21", "v0.36.20"]);
    const first = within(sections[0] as HTMLElement);
    expect(first.getByText("October 18, 2026")).toBeInTheDocument();
    expect(first.getByRole("heading", { level: 3, name: "Fixes" })).toBeInTheDocument();
    expect(first.getByRole("link", { name: "Release on GitHub" })).toHaveAttribute("href", releases[0].url);
    expect(within(sections[1] as HTMLElement).queryByRole("link", { name: "Release on GitHub" })).toBeNull();
    expect(screen.getByText(/not affiliated with Rivian/)).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: /feed/i })[0]).toHaveAttribute("href", "/changelog.atom");
  });

  it("never renders raw HTML from the notes", async () => {
    changelog.mockResolvedValue({ releases });
    const { container } = renderPage();
    await screen.findByRole("heading", { level: 2, name: /v0\.36\.21/ });
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("script")).toBeNull();
    // Dropped, not shown as stray text either.
    expect(container.textContent).not.toContain("onerror");
    expect(container.textContent).not.toContain("<script");
  });

  // A "//host" target is another site written without a scheme: the renderer does not link it (only https, mailto and this site's own paths
  // are linked), so the text stays and there is nothing to click. An https link opens in a new tab with noopener.
  it("does not link a protocol-relative target, and opens an https one safely", async () => {
    changelog.mockResolvedValue({ releases: [{ ...releases[1], body_md: "- See [elsewhere](//evil.example/x), [docs](https://docs.example/x) and [planner](/plan)." }] });
    renderPage();
    expect(await screen.findByText(/elsewhere/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "elsewhere" })).toBeNull();
    const ext = screen.getByRole("link", { name: "docs" });
    expect(ext).toHaveAttribute("rel", "noopener noreferrer");
    expect(ext).toHaveAttribute("target", "_blank");
    expect(screen.getByRole("link", { name: "planner" })).not.toHaveAttribute("target");
  });

  it("says so when the notes are unavailable", async () => {
    changelog.mockRejectedValue(new Error("503"));
    renderPage();
    expect(await screen.findByText(/unavailable right now/, undefined, { timeout: 5000 })).toBeInTheDocument();
  });
});
