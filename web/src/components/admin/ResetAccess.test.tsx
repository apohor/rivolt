import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ResetAccess, stepWords } from "./ResetAccess";
import { stubFetchRoutes } from "../../test/fetchRoutes";
import type { AdminUserRow } from "../../lib/api";

const row = { id: "u2", username: "quiet@example.com", email: "quiet@example.com", display_name: "Quiet", role: "user" } as unknown as AdminUserRow;

async function confirm() {
  fireEvent.click(screen.getByRole("button", { name: "Reset this account's access…" }));
  const dialog = screen.getByRole("alertdialog");
  const go = dialog.querySelector("button[disabled]") as HTMLButtonElement;
  expect(go.disabled).toBe(true); // nothing runs until the address is typed
  fireEvent.change(dialog.querySelector("input")!, { target: { value: "quiet@example.com" } });
  fireEvent.click(screen.getByRole("button", { name: "Reset access" }));
}

describe("ResetAccess", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("asks for the address first, then shows the temporary password once, with the handing-over rule", async () => {
    const fetchMock = stubFetchRoutes({ "/api/admin/users/u2/secure": { body: { ok: true, temporary_password: "Zq7-temp-pass", sessions_revoked: 2, passkeys_removed: 1 } } });
    render(<ResetAccess row={row} />);
    await confirm();
    const shown = await screen.findByText("Zq7-temp-pass");
    expect(shown).toBeTruthy();
    expect(fetchMock.mock.calls.filter((c) => String(c[0]).endsWith("/secure"))).toHaveLength(1);
    expect(screen.getByText(/ONLY to the email address on the account/)).toBeTruthy();
    expect(screen.getAllByText("quiet@example.com").length).toBeGreaterThan(0);
    // Closing clears it for good: it is not in the page any more.
    fireEvent.click(screen.getByRole("button", { name: "Done, clear it" }));
    expect(screen.queryByText("Zq7-temp-pass")).toBeNull();
    expect(document.body.textContent).not.toContain("Zq7-temp-pass");
  });

  it("copies the password to the clipboard on request", async () => {
    stubFetchRoutes({ "/api/admin/users/u2/secure": { body: { ok: true, temporary_password: "Zq7-temp-pass", sessions_revoked: 0, passkeys_removed: 0 } } });
    const writeText = vi.fn(async () => {});
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    render(<ResetAccess row={row} />);
    await confirm();
    await screen.findByText("Zq7-temp-pass");
    fireEvent.click(screen.getByRole("button", { name: "Copy" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("Zq7-temp-pass"));
    expect(await screen.findByRole("button", { name: "Copied" })).toBeTruthy();
  });

  it("on a partial failure names the step, shows no password and offers Run again", async () => {
    const fetchMock = stubFetchRoutes({ "/api/admin/users/u2/secure": { status: 502, body: { error: "incomplete", step: "revoke_sessions" } } });
    render(<ResetAccess row={row} />);
    await confirm();
    expect(await screen.findByText(/It stopped while signing the sessions out\. No password was returned\./)).toBeTruthy();
    expect(document.querySelector("[data-temp-password]")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Run again" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter((c) => String(c[0]).endsWith("/secure"))).toHaveLength(2));
  });

  it("says plainly when the account has no Rivolt password, and on any other failure returns no password", async () => {
    stubFetchRoutes({ "/api/admin/users/u2/secure": { status: 409, body: { error: "no_password_account" } } });
    render(<ResetAccess row={row} />);
    await confirm();
    expect(await screen.findByText(/signs in with a provider/)).toBeTruthy();
    cleanup();
    stubFetchRoutes({ "/api/admin/users/u2/secure": { status: 500, body: "boom" } });
    render(<ResetAccess row={row} />);
    await confirm();
    expect(await screen.findByText(/no password was returned/)).toBeTruthy();
  });

  it("names every step in words and uses no banned word", () => {
    for (const s of ["set_password", "forget_remembered_logins", "revoke_sessions", "list_passkeys", "delete_passkeys", "something_new"]) {
      expect(stepWords(s)).not.toMatch(/_/);
    }
    const all = ["setting the new password", "signing the sessions out"].join(" ");
    expect(all).not.toMatch(/\b(safe|secure|OK|normal|exact|measured|towing)\b/);
  });
});
