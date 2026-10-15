import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StatementEmailCard } from "./SettingsPage";

function mockFetch(settings: object | null, preview?: object) {
  const calls: string[] = [];
  bodies.length = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      calls.push(`${init?.method ?? "GET"} ${url}`);
      const j = (b: unknown) =>
        ({ ok: true, status: 200, headers: { get: () => "application/json" }, text: async () => JSON.stringify(b) }) as unknown as Response;
      const nf = { ok: false, status: 404, headers: { get: () => "application/json" }, text: async () => "{}" } as unknown as Response;
      if (url.startsWith("/api/settings/statement-email")) {
        if (settings == null) return nf;
        if (init?.method === "PUT") bodies.push(String(init.body));
        if (init?.method === "PUT") return j({ ...settings, enabled: JSON.parse(String(init.body)).enabled });
        return j(settings);
      }
      if (url.startsWith("/api/statement-email/preview")) return preview ? j(preview) : nf;
      return nf;
    }),
  );
  return calls;
}

const bodies: string[] = [];

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <StatementEmailCard />
    </QueryClientProvider>,
  );
}

describe("StatementEmailCard", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("is hidden when the endpoint is absent or the feature isn't available", async () => {
    mockFetch(null);
    const a = renderCard();
    await vi.waitFor(() => expect(a.container.textContent).toBe(""));
    cleanup();
    mockFetch({ enabled: false, available: false });
    const b = renderCard();
    await new Promise((r) => setTimeout(r, 50));
    expect(b.container.textContent).toBe("");
  });

  it("is off by default and turns on with one click", async () => {
    const calls = mockFetch({ enabled: false, available: true });
    renderCard();
    const sw = await screen.findByRole("switch");
    expect(sw).toHaveAttribute("aria-checked", "false");
    fireEvent.click(sw);
    await vi.waitFor(() => expect(screen.getByRole("switch")).toHaveAttribute("aria-checked", "true"));
    expect(calls.some((c) => c.startsWith("PUT /api/settings/statement-email"))).toBe(true);
    // The browser's timezone goes with the opt-in.
    const sent = bodies.find((b) => b.includes("enabled"));
    expect(JSON.parse(sent!).timezone).toBe(Intl.DateTimeFormat().resolvedOptions().timeZone);
  });

  it("previews last month's email without sending", async () => {
    mockFetch(
      { enabled: false, available: true },
      { subject: "Your Rivian cost for September: $88.00", text: "Hi there,\nYou spent $88.00", html: "<p>x</p>", will_send: false, reason: "Turned off" },
    );
    renderCard();
    fireEvent.click(await screen.findByRole("button", { name: /Preview last month/ }));
    expect(await screen.findByText(/Your Rivian cost for September/)).toBeInTheDocument();
    expect(screen.getByText(/You spent \$88\.00/)).toBeInTheDocument();
    expect(screen.getByText("Turned off")).toBeInTheDocument();
  });
});
