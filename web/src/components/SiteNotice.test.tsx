import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { activeNotice, SiteNoticeBanner, SiteNoticeLine } from "./SiteNotice";

const NOTICE = {
  id: "transfer-2026-12-01",
  text: "From December 1, 2026, Rivolt will be run by ZeroDowntime LLC. Nothing changes for your data; details in the email we sent, or export or delete your data in Settings → Data.",
  starts_at: "2026-10-01T00:00:00Z",
  until: "2099-12-01T00:00:00Z",
  link_text: "Settings → Data",
  link_url: "/settings?tab=data",
};

function mockMe(notice: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok: true,
      status: 200,
      headers: { get: () => "application/json" },
      text: async () => JSON.stringify({ user_id: "u", username: "u", role: "user", onboarding_completed: true, notice }),
    }) as unknown as Response),
  );
}

function wrap(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <MemoryRouter>
      <QueryClientProvider client={qc}>{node}</QueryClientProvider>
    </MemoryRouter>,
  );
}

describe("site notice", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    localStorage.clear();
  });

  it("shows only inside its window", () => {
    expect(activeNotice(null)).toBeNull();
    expect(activeNotice(NOTICE, Date.parse("2026-09-30T00:00:00Z"))).toBeNull();
    expect(activeNotice(NOTICE, Date.parse("2026-11-02T00:00:00Z"))?.id).toBe(NOTICE.id);
    expect(activeNotice({ ...NOTICE, until: "2026-12-01T00:00:00Z" }, Date.parse("2026-12-01T00:00:00Z"))).toBeNull();
  });

  it("is a dismissible banner, per device, while Settings keeps the line", async () => {
    mockMe(NOTICE);
    wrap(
      <>
        <SiteNoticeBanner />
        <SiteNoticeLine />
      </>,
    );
    const region = await screen.findByRole("region", { name: "Notice" });
    expect(region).toHaveTextContent("From December 1, 2026, Rivolt will be run by ZeroDowntime LLC.");
    expect(screen.getAllByRole("link", { name: "Settings → Data" })[0]).toHaveAttribute("href", "/settings?tab=data");
    fireEvent.click(screen.getByRole("button", { name: "Dismiss notice" }));
    expect(screen.queryByRole("region", { name: "Notice" })).toBeNull();
    expect(localStorage.getItem("rivolt:notice-dismissed:transfer-2026-12-01")).toBe("1");
    expect(document.querySelector("[data-site-notice-line]")).toHaveTextContent("ZeroDowntime LLC");
    cleanup();
    // Dismissed on this device: the banner stays away on the next page.
    wrap(<SiteNoticeBanner />);
    await waitFor(() => expect(document.querySelector("[data-site-notice]")).toBeNull());
  });

  it("shows nothing without a notice (or from an older server)", async () => {
    mockMe(null);
    wrap(
      <>
        <SiteNoticeBanner />
        <SiteNoticeLine />
      </>,
    );
    await new Promise((r) => setTimeout(r, 20));
    expect(document.querySelector("[data-site-notice]")).toBeNull();
    expect(document.querySelector("[data-site-notice-line]")).toBeNull();
  });
});
