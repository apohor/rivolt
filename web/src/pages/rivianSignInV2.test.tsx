import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";

type Route = { status?: number; body: unknown };

// The onboarding connect step with the key on (or off), the Rivian status and the login/mfa/logout answers chosen per case.
async function mount(opts: { keys?: string[]; mfa?: boolean; login?: Route; mfaAnswer?: Route; demo?: boolean } = {}) {
  vi.resetModules();
  const fn = stubFetchRoutes({
    "/api/config": { body: { features: { signup_enabled: true, demo_enabled: !!opts.demo, design_preview: opts.keys ?? ["signup_v2"] } } },
    "/api/auth/me": { body: { id: "u", email: "a@b.c", onboarding_completed: false } },
    "/api/settings/rivian": { body: { enabled: true, authenticated: false, mfa_pending: !!opts.mfa } },
    "/api/settings/rivian/login": opts.login ?? { body: { ok: true } },
    "/api/settings/rivian/mfa": opts.mfaAnswer ?? { body: { ok: true } },
    "/api/settings/rivian/logout": { body: { ok: true } },
    "/api/events": { body: {} },
  });
  const { default: Page } = await import("./OnboardingPage");
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={["/onboarding?connect=1"]}>
        <Page />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return fn;
}

const fill = (email = "me@example.com", password = "pw-1234") => {
  fireEvent.change(screen.getByLabelText("Rivian email"), { target: { value: email } });
  fireEvent.change(screen.getByLabelText("Rivian password"), { target: { value: password } });
};
const posted = (fn: ReturnType<typeof stubFetchRoutes>, path: string) => fn.mock.calls.filter(([u, i]) => String(u).includes(path) && (i as RequestInit | undefined)?.method === "POST");

describe("signup_v2 Rivian sign-in", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("one screen: labelled Rivian fields with password-manager hints, the safer option before the password, only true assurances", async () => {
    await mount();
    const h = await screen.findByRole("heading", { name: "Sign in to Rivian" });
    expect(document.activeElement).toBe(h);
    const email = screen.getByLabelText("Rivian email") as HTMLInputElement;
    const pw = screen.getByLabelText("Rivian password") as HTMLInputElement;
    expect(email.getAttribute("autocomplete")).toBe("username");
    expect(email.type).toBe("email");
    expect(pw.getAttribute("autocomplete")).toBe("current-password");
    const safer = document.querySelector("[data-connect-safer]") as HTMLElement;
    expect(safer.textContent).toMatch(/Safer option: connect a separate Authorized Driver account/);
    expect(safer.compareDocumentPosition(pw) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByRole("link", { name: "How to set one up" }).getAttribute("href")).toBe("/driver-account");
    expect(document.body.textContent).not.toMatch(/recommend/i);
    expect(document.body.textContent).toContain("Read-only: Rivolt never sends commands to your truck.");
    expect(document.body.textContent).toContain("Your password is used once and not stored. Rivolt keeps your Rivian email and an encrypted session token.");
    expect(screen.getByRole("link", { name: "Is it safe to connect?" }).getAttribute("href")).toBe("/safe-to-connect");
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
  });

  it("key off: today's connect form, unchanged", async () => {
    await mount({ keys: [] });
    expect(await screen.findByRole("heading", { name: "Connect your Rivian" })).toBeTruthy();
    expect(screen.queryByLabelText("Rivian email")).toBeNull();
    expect(await screen.findByRole("radiogroup", { name: "Which account" })).toBeTruthy();
  });

  it("signs in with the typed email and password", async () => {
    const fn = await mount();
    await screen.findByLabelText("Rivian email");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Sign in to Rivian" }));
    await waitFor(() => expect(posted(fn, "/api/settings/rivian/login")).toHaveLength(1));
    expect(JSON.parse(String((posted(fn, "/api/settings/rivian/login")[0][1] as RequestInit).body))).toEqual({ email: "me@example.com", password: "pw-1234" });
  });

  it.each([
    ["user_action", "Rivian didn't accept that email or password", "Try again"],
    ["transient", "Rivian isn't answering right now", "Try again"],
    ["rate_limited", "Too many tries right now", null],
  ])("a %s failure says what to do", async (cls, title, action) => {
    const fn = await mount({ login: { status: 400, body: { error: "x", class: cls } }, demo: true });
    await screen.findByLabelText("Rivian email");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Sign in to Rivian" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain(title);
    if (action) fireEvent.click(screen.getByRole("button", { name: action }));
    else expect(screen.queryByRole("button", { name: "Try again" })).toBeNull();
    if (action) await waitFor(() => expect(posted(fn, "/api/settings/rivian/login")).toHaveLength(2));
    if (cls === "transient") expect(screen.getByRole("link", { name: "look at the sample truck" }).getAttribute("href")).toBe("/demo?from=app");
    // The details stay filled in.
    expect((screen.getByLabelText("Rivian email") as HTMLInputElement).value).toBe("me@example.com");
  });

  it("the code step: focus on its heading, one-time-code field, a wrong code offers Get a new code, which starts again", async () => {
    const fn = await mount({ mfa: true, mfaAnswer: { status: 400, body: { error: "x", class: "user_action" } } });
    const h = await screen.findByRole("heading", { name: "Enter the code from Rivian" });
    expect(document.activeElement).toBe(h);
    const code = screen.getByLabelText("Code") as HTMLInputElement;
    expect(code.getAttribute("autocomplete")).toBe("one-time-code");
    fireEvent.change(code, { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Finish signing in" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("That code didn't work");
    fireEvent.click(screen.getByRole("button", { name: "Get a new code" }));
    await waitFor(() => expect(posted(fn, "/api/settings/rivian/logout")).toHaveLength(1));
  });
});
