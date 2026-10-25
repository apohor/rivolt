import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { PasskeysCard } from "./PasskeysCard";
import { stubFetchRoutes } from "../test/fetchRoutes";

function renderCard() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <PasskeysCard />
    </QueryClientProvider>,
  );
}
const me = (extra: object) => ({ "/api/auth/me": { body: { authenticated: true, user_id: "u1", username: "owner", role: "user", onboarding_completed: true, ...extra } } });

describe("PasskeysCard", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("makes no passkey request, and shows nothing, where the account says passkeys are off", async () => {
    const fetchMock = stubFetchRoutes({ ...me({}), "/api/auth/passkeys": { status: 404, body: "not found" } });
    renderCard();
    await waitFor(() => expect(fetchMock.mock.calls.some((c) => String(c[0]).includes("/api/auth/me"))).toBe(true));
    await new Promise((r) => setTimeout(r, 50));
    expect(fetchMock.mock.calls.some((c) => String(c[0]).includes("/api/auth/passkeys"))).toBe(false);
    expect(screen.queryByText("Passkeys")).toBeNull();
  });

  it("asks for the list and shows the card where the account says they are on", async () => {
    const fetchMock = stubFetchRoutes({ ...me({ passkeys: true }), "/api/auth/passkeys": { body: { passkeys: [], max: 10 } } });
    renderCard();
    expect(await screen.findByText("No passkeys yet.")).toBeTruthy();
    expect(fetchMock.mock.calls.some((c) => String(c[0]).includes("/api/auth/passkeys"))).toBe(true);
  });
});
