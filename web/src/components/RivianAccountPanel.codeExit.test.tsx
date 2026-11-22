import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RivianAccountPanel } from "./RivianAccountPanel";
import { stubFetchRoutes } from "../test/fetchRoutes";

// N10: every way out of the code step (Start again, Get a new code, Cancel) must show the sign-in form again, not leave the code form until a reload.
function mount(v2: boolean, mfaStatus = 200) {
  stubFetchRoutes({
    "/api/settings/rivian": { body: { enabled: true, authenticated: false, mfa_pending: false } },
    "/api/settings/rivian/login": { body: { authenticated: false, mfa_pending: true } },
    "/api/settings/rivian/mfa": { status: mfaStatus, body: { error: "invalid code" } },
    "/api/settings/rivian/logout": { body: { authenticated: false } },
    "/api/events": { body: {} },
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <RivianAccountPanel v2={v2} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}
async function toCodeStep(v2: boolean) {
  if (v2) {
    fireEvent.change(await screen.findByLabelText("Rivian email"), { target: { value: "d@example.com" } });
    fireEvent.change(screen.getByLabelText("Rivian password"), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: "Sign in to Rivian" }));
    await screen.findByLabelText("Code");
  } else {
    fireEvent.change(await screen.findByPlaceholderText("you@example.com"), { target: { value: "d@example.com" } });
    fireEvent.change(screen.getByPlaceholderText("Password"), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: /^Sign in/ }));
    await screen.findByPlaceholderText("123456");
  }
}
async function wrongCode(v2: boolean) {
  const box = v2 ? screen.getByLabelText("Code") : screen.getByPlaceholderText("123456");
  fireEvent.change(box, { target: { value: "123456" } });
  fireEvent.click(screen.getByRole("button", { name: v2 ? "Finish signing in" : "Verify" }));
}
const backOnForm = (v2: boolean) =>
  waitFor(() => expect(v2 ? screen.getByLabelText("Rivian email") : screen.getByPlaceholderText("you@example.com")).toBeInTheDocument());

describe("leaving the Rivian code step", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("new form: No code? Start again", async () => {
    mount(true);
    await toCodeStep(true);
    fireEvent.click(screen.getByRole("button", { name: "No code? Start again" }));
    await backOnForm(true);
    expect(screen.queryByLabelText("Code")).toBeNull();
  });

  it("new form: Get a new code after a wrong code", async () => {
    mount(true, 401);
    await toCodeStep(true);
    await wrongCode(true);
    fireEvent.click(await screen.findByRole("button", { name: "Get a new code" }));
    await backOnForm(true);
    expect(screen.queryByLabelText("Code")).toBeNull();
  });

  it("old form: Cancel", async () => {
    mount(false);
    await toCodeStep(false);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await backOnForm(false);
    expect(screen.queryByPlaceholderText("123456")).toBeNull();
  });

  it("old form: Start again for a new code after a wrong code", async () => {
    mount(false, 401);
    await toCodeStep(false);
    await wrongCode(false);
    fireEvent.click(await screen.findByRole("button", { name: "Start again for a new code" }));
    await backOnForm(false);
    expect(screen.queryByPlaceholderText("123456")).toBeNull();
  });
});
