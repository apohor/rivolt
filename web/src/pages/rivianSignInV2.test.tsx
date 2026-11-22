import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";

const trackMock = vi.hoisted(() => vi.fn());
vi.mock("../lib/track", () => ({ track: trackMock }));

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

  it("the short card: its words, nothing else; labelled fields, the safer option before the password", async () => {
    await mount({ demo: true });
    const h = await screen.findByRole("heading", { name: "Connect your Rivian" });
    expect(document.activeElement).toBe(h);
    const email = screen.getByLabelText("Rivian email") as HTMLInputElement;
    const pw = screen.getByLabelText("Rivian password") as HTMLInputElement;
    expect(email.getAttribute("autocomplete")).toBe("username");
    expect(email.type).toBe("email");
    expect(pw.getAttribute("autocomplete")).toBe("current-password");
    const safer = document.querySelector("[data-connect-safer]") as HTMLElement;
    expect(safer.textContent).toBe("Safer: use a separate driver account. How");
    expect(safer.compareDocumentPosition(pw) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(screen.getByRole("link", { name: "How" }).getAttribute("href")).toBe("/driver-account");
    const text = document.body.textContent ?? "";
    expect(text).toContain("Use your Rivian app login. Read-only, and your password is never stored.");
    expect(text).toContain("Rivolt records your truck's location and vehicle data.");
    expect(screen.getByRole("link", { name: "Privacy" }).getAttribute("href")).toBe("/privacy");
    expect(text).toContain("Unofficial, not affiliated with Rivian.");
    expect(screen.getByRole("link", { name: "Is it safe?" }).getAttribute("href")).toBe("/safe-to-connect");
    expect(screen.getAllByRole("link", { name: "Try a sample truck" })).toHaveLength(1);
    // Gone from the card: the trust paragraph, the bullets and box, the context line, the checkbox and radios, per-field hints.
    expect(text).not.toMatch(/recommend|Authorized Driver|Disconnect any time|encrypted session token|reads your truck's data/i);
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.queryByRole("radio")).toBeNull();
    expect(email.placeholder).toBe("");
  });

  it("the sign-in event carries no authorized_driver property with the key on; the key-off form still sends it", async () => {
    trackMock.mockClear();
    await mount();
    await screen.findByLabelText("Rivian email");
    fill();
    fireEvent.click(screen.getByRole("button", { name: "Sign in to Rivian" }));
    await waitFor(() => expect(trackMock).toHaveBeenCalledWith("rivian_connect_start", {}));
    expect(trackMock.mock.calls.some(([, p]) => p && "authorized_driver" in p)).toBe(false);
    cleanup();
    trackMock.mockClear();
    await mount({ keys: [] });
    fireEvent.change(await screen.findByPlaceholderText("you@example.com"), { target: { value: "me@example.com" } });
    fireEvent.change(screen.getByPlaceholderText("Password"), { target: { value: "pw-1234" } });
    fireEvent.click(screen.getByRole("button", { name: /^Sign in/ }));
    await waitFor(() => expect(trackMock).toHaveBeenCalledWith("rivian_connect_start", { authorized_driver: true }));
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
