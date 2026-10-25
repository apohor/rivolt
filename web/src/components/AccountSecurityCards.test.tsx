import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Component, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { AccountSecurityCards } from "./AccountSecurityCards";
import { stubFetchRoutes } from "../test/fetchRoutes";

// A render error (a hook count that changed, say) lands here, so a crash cannot pass for "the cards went away".
class Boundary extends Component<{ children: ReactNode }, { crashed: boolean }> {
  state = { crashed: false };
  static getDerivedStateFromError() {
    return { crashed: true };
  }
  render() {
    return this.state.crashed ? <div data-crashed /> : this.props.children;
  }
}

function renderCards() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <Boundary>
        <AccountSecurityCards />
      </Boundary>
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
    expect(document.querySelector("[data-crashed]")).toBeNull();
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

describe("Remove all passkeys", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("is disabled while it runs, so a double click cannot start two delete loops", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((r) => (release = r));
    const deletes: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const path = String(input).split("?")[0];
        const json = (status: number, body: unknown) => ({ ok: status < 300, status, headers: { get: () => "application/json" }, text: async () => JSON.stringify(body), json: async () => body }) as unknown as Response;
        if (path === "/api/auth/secure") return json(502, { error: "password_changed_but_sign_out_incomplete", password_changed: true, incomplete: ["passkeys"] });
        if (path === "/api/auth/passkeys" && (init?.method ?? "GET") === "GET") return json(200, { passkeys: [{ id: "p1" }, { id: "p2" }], max: 10 });
        if (path.startsWith("/api/auth/passkeys/") && init?.method === "DELETE") {
          deletes.push(path);
          await gate;
          return { ok: true, status: 204, headers: { get: () => null }, text: async () => "", json: async () => ({}) } as unknown as Response;
        }
        return json(404, {});
      }),
    );
    renderCards();
    const form = document.querySelector("[data-security-form=reset]") as HTMLElement;
    fireEvent.change(form.querySelector("input[autocomplete=current-password]")!, { target: { value: "old-pass-1" } });
    const news = form.querySelectorAll("input[autocomplete=new-password]");
    fireEvent.change(news[0], { target: { value: "new-pass-2" } });
    fireEvent.change(news[1], { target: { value: "new-pass-2" } });
    fireEvent.submit(form); // first press asks again
    fireEvent.submit(form); // second press does it
    const button = (await screen.findByRole("button", { name: "Remove all passkeys" })) as HTMLButtonElement;
    fireEvent.click(button);
    await waitFor(() => expect(button.disabled).toBe(true));
    fireEvent.click(button);
    fireEvent.click(button);
    release();
    await waitFor(() => expect(screen.getByText("All passkeys removed.")).toBeTruthy());
    expect(deletes).toEqual(["/api/auth/passkeys/p1", "/api/auth/passkeys/p2"]);
  });
});
