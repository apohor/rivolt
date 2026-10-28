import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import LoginPage from "./LoginPage";
import HydraLoginPage from "./HydraLoginPage";
import { stubFetchRoutes } from "../test/fetchRoutes";
import { SESSION_EXPIRED_TEXT, readSigninEmail, rememberSigninEmail, rememberSigninProvider } from "../lib/signinDraft";

const assign = vi.fn();
const originalLocation = window.location;

beforeEach(() => {
  assign.mockReset();
  Object.defineProperty(window, "location", { configurable: true, value: { ...originalLocation, origin: "https://rivolt.test", pathname: "/login", search: "", assign } });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
  Object.defineProperty(window, "location", { configurable: true, value: originalLocation });
});

const providers = [
  { name: "hydra", display_name: "Email", start_url: "/api/auth/oidc/hydra/start" },
  { name: "google", display_name: "Google", start_url: "/api/auth/oidc/google/start" },
];

function login(url: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[url]}>
        <LoginPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("a sign-in that could not be finished", () => {
  const routes = () =>
    stubFetchRoutes({
      "/api/auth/oidc": { body: providers },
      "/api/auth/me": { status: 401, body: { error: "unauthorized" } },
      "/api/config": { body: {} },
    });

  it("says what happened and offers to start again", async () => {
    routes();
    login("/login?error=session_expired");
    expect(await screen.findByText(SESSION_EXPIRED_TEXT)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start sign-in again" })).toBeInTheDocument();
  });

  it("starts the provider chosen last again", async () => {
    routes();
    rememberSigninProvider("hydra");
    login("/login?error=session_expired");
    await screen.findByRole("button", { name: "Continue with Email" });
    fireEvent.click(screen.getByRole("button", { name: "Start sign-in again" }));
    expect(assign).toHaveBeenCalledTimes(1);
    expect(String(assign.mock.calls[0][0])).toContain("/api/auth/oidc/hydra/start");
  });

  it("with no provider to repeat, goes back to the page without the error", async () => {
    routes();
    login("/login?error=session_expired");
    await screen.findByRole("button", { name: "Continue with Email" });
    fireEvent.click(screen.getByRole("button", { name: "Start sign-in again" }));
    expect(assign).not.toHaveBeenCalled();
    expect(screen.queryByText(SESSION_EXPIRED_TEXT)).not.toBeInTheDocument();
  });

  it("does not offer a restart for other errors", async () => {
    routes();
    login("/login?error=account_disabled");
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start sign-in again" })).not.toBeInTheDocument();
  });

  it("remembers the provider when one is chosen", async () => {
    routes();
    login("/login");
    fireEvent.click(await screen.findByRole("button", { name: "Continue with Google" }));
    expect(window.sessionStorage.getItem("rivolt.signin.provider")).toBe("google");
  });
});

describe("the email form across a restart or reload", () => {
  const challenge = { challenge: "c1", client_id: "rivolt", client_name: "Rivolt", requested_scope: [], passkeys: false };
  const form = () => {
    stubFetchRoutes({ "/api/auth/hydra/login": { body: challenge } });
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
      <QueryClientProvider client={qc}>
        <MemoryRouter initialEntries={["/auth/hydra/login?login_challenge=c1"]}>
          <HydraLoginPage />
        </MemoryRouter>
      </QueryClientProvider>,
    );
  };

  it("fills the email back in and never the password", async () => {
    rememberSigninEmail("me@example.com");
    form();
    const email = (await screen.findByLabelText(/email/i)) as HTMLInputElement;
    expect(email.value).toBe("me@example.com");
    expect((screen.getByLabelText(/password/i) as HTMLInputElement).value).toBe("");
  });

  it("keeps what is typed for a reload, and stores no password", async () => {
    form();
    const email = await screen.findByLabelText(/email/i);
    fireEvent.change(email, { target: { value: "typed@example.com" } });
    fireEvent.change(screen.getByLabelText(/password/i), { target: { value: "hunter2" } });
    expect(readSigninEmail()).toBe("typed@example.com");
    expect(JSON.stringify({ ...window.sessionStorage })).not.toContain("hunter2");
  });
});
