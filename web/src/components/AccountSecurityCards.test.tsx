import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AccountSecurityCards } from "./AccountSecurityCards";
import { stubFetchRoutes } from "../test/fetchRoutes";

function renderCards() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <AccountSecurityCards />
    </QueryClientProvider>,
  );
}

function fillAndSubmit() {
  const form = document.querySelector("[data-security-form=password]") as HTMLElement;
  fireEvent.change(form.querySelector("input[autocomplete=current-password]")!, { target: { value: "old-pass-1" } });
  const news = form.querySelectorAll("input[autocomplete=new-password]");
  fireEvent.change(news[0], { target: { value: "new-pass-2" } });
  fireEvent.change(news[1], { target: { value: "new-pass-2" } });
  fireEvent.submit(form);
}

describe("AccountSecurityCards", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("hides both cards when the account has no Rivolt password, without crashing", async () => {
    stubFetchRoutes({ "/api/auth/password": { status: 409, body: { error: "needs_an_email_account" } } });
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    renderCards();
    expect(document.querySelector("[data-security-form=password]")).not.toBeNull();
    fillAndSubmit();
    await waitFor(() => expect(document.querySelector("[data-security-form]")).toBeNull());
    // React reports a hooks-order or render error through console.error; there must be none.
    expect(errors.mock.calls.filter((c) => String(c[0]).includes("hooks") || String(c[0]).includes("Rendered fewer"))).toEqual([]);
    errors.mockRestore();
  });

  it("shows the wrong-password line and keeps the form", async () => {
    stubFetchRoutes({ "/api/auth/password": { status: 403, body: { error: "password_incorrect" } } });
    renderCards();
    fillAndSubmit();
    expect(await screen.findByText("That isn't your current password.")).toBeTruthy();
    expect(document.querySelector("[data-security-form=password]")).not.toBeNull();
  });
});
