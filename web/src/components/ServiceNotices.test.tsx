import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { ServiceNoticeBanner, ServiceNoticesCard } from "./ServiceNotices";
import { bannerNotice, noticeHref } from "../lib/serviceNotices";
import { stubFetchRoutes } from "../test/fetchRoutes";

const notice = (over: Record<string, unknown> = {}) => ({
  kind: "pro_launch",
  title: "Rivolt Pro starts on a date",
  body: "What stays free, and what Pro adds.",
  links: [
    { label: "Read more", href: "/support" },
    { label: "Docs", href: "https://example.org/docs" },
    { label: "Bad", href: "javascript:alert(1)" },
  ],
  shows_until: "2027-01-01T00:00:00Z",
  dismissed: false,
  ...over,
});

function mount(list: unknown[], ui: React.ReactNode) {
  const fetchMock = stubFetchRoutes({ "/api/notices": { body: list }, "/api/notices/dismiss": { status: 204, body: "" } });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
  return fetchMock;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("service notices", () => {
  it("shows the first undismissed notice on Now, with only safe links", async () => {
    mount([notice({ kind: "old", title: "Old", dismissed: true }), notice()], <ServiceNoticeBanner />);
    expect(await screen.findByText("Rivolt Pro starts on a date")).toBeTruthy();
    expect(screen.queryByText("Old")).toBeNull();
    expect(screen.getByText("Read more").getAttribute("href")).toBe("/support");
    expect(screen.getByText("Docs").getAttribute("rel")).toContain("noopener");
    expect(screen.queryByText("Bad")).toBeNull();
  });

  it("hides the banner on Dismiss and tells the server", async () => {
    const f = mount([notice()], <ServiceNoticeBanner />);
    fireEvent.click(await screen.findByText("Dismiss"));
    await waitFor(() => expect(screen.queryByText("Rivolt Pro starts on a date")).toBeNull());
    const call = (f as unknown as { mock: { calls: unknown[][] } }).mock.calls.find((c) => String(c[0]).includes("/api/notices/dismiss"));
    expect(call).toBeTruthy();
  });

  it("shows nothing with no notices, and the Settings row keeps a dismissed notice readable", async () => {
    mount([], <ServiceNoticeBanner />);
    await new Promise((r) => setTimeout(r, 30));
    expect(document.querySelector("[data-service-notice]")).toBeNull();
    cleanup();
    mount([notice({ dismissed: true })], <><ServiceNoticeBanner /><ServiceNoticesCard /></>);
    expect(await screen.findByText("What stays free, and what Pro adds.")).toBeTruthy();
    expect(document.querySelector("[data-service-notice]")).toBeNull();
  });
});

describe("notice helpers", () => {
  it("picks the first undismissed and accepts only paths and https", () => {
    expect(bannerNotice(undefined)).toBeNull();
    expect(noticeHref("//evil.example")).toBeNull();
    expect(noticeHref("http://example.org")).toBeNull();
    expect(noticeHref("/settings#plan")).toEqual({ kind: "internal", href: "/settings#plan" });
  });
});
