import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";
import { SignOutButton } from "./SignOutButton";
import { FROM_APP_KEY } from "../lib/demoFromApp";

// The sample truck's "came from my account" mark ends with the sign-out, so a signed-out visitor in the same tab gets sign-up again.
describe("sign out and the sample truck's from-app mark", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    sessionStorage.clear();
  });

  it("signing out forgets the mark", async () => {
    stubFetchRoutes({ "/api/auth/me": { body: { user_id: "u1", username: "a@b.c", role: "user" } }, "/api/auth/logout": { body: { ok: true } } });
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    sessionStorage.setItem(FROM_APP_KEY, "1");
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <SignOutButton />
      </QueryClientProvider>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("/login"));
    expect(sessionStorage.getItem(FROM_APP_KEY)).toBeNull();
  });
});
