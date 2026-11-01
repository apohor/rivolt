import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { AppleLinkCard } from "./AppleLinkCard";
import { APPLE_LINK_COPY as copy } from "../content/appleLinkCopy";
import { stubFetchRoutes } from "../test/fetchRoutes";

function renderCard(go = vi.fn(), at = "/settings") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[at]}>
        <AppleLinkCard go={go} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return go;
}
const status = (over: object = {}) => ({ body: { available: true, linked: false, has_password: true, ...over } });
const typePassword = (pw: string) => fireEvent.change(screen.getByLabelText(copy.passwordLabel), { target: { value: pw } });
const posted = (fetchMock: ReturnType<typeof stubFetchRoutes>, path: string) => fetchMock.mock.calls.filter((c) => String(c[0]).includes(path) && (c[1] as RequestInit | undefined)?.method === "POST");

describe("AppleLinkCard", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("shows nothing where linking is off (404) and for the app or a viewing-as admin (403)", async () => {
    for (const code of [404, 403]) {
      const fetchMock = stubFetchRoutes({ "/api/auth/apple-link": { status: code, body: { error: "apple_not_available" } } });
      renderCard();
      await waitFor(() => expect(fetchMock.mock.calls.some((c) => String(c[0]).includes("/api/auth/apple-link"))).toBe(true));
      await new Promise((r) => setTimeout(r, 30));
      expect(screen.queryByText(copy.title)).toBeNull();
      cleanup();
    }
  });

  it("connects: asks for the password, posts it, and sends the browser to the address the server gives", async () => {
    const fetchMock = stubFetchRoutes({
      "/api/auth/apple-link/start": { body: { url: "https://appleid.example/authorize?state=x" } },
      "/api/auth/apple-link": status(),
    });
    const go = renderCard();
    expect(await screen.findByText(copy.connectLine)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: copy.connect }));
    expect(screen.getByText(copy.connectPromptTitle)).toBeTruthy();
    typePassword("right");
    fireEvent.click(screen.getByRole("button", { name: copy.continueToApple }));
    await waitFor(() => expect(go).toHaveBeenCalledWith("https://appleid.example/authorize?state=x"));
    expect(JSON.parse(String((posted(fetchMock, "/api/auth/apple-link/start")[0][1] as RequestInit).body))).toEqual({ password: "right" });
  });

  it("a wrong password is said plainly, the field is cleared and the browser goes nowhere", async () => {
    stubFetchRoutes({
      "/api/auth/apple-link/start": { status: 403, body: { error: "password_incorrect" } },
      "/api/auth/apple-link": status(),
    });
    const go = renderCard();
    fireEvent.click(await screen.findByRole("button", { name: copy.connect }));
    typePassword("wrong");
    fireEvent.click(screen.getByRole("button", { name: copy.continueToApple }));
    expect(await screen.findByText(copy.failures.password_incorrect)).toBeTruthy();
    expect((screen.getByLabelText(copy.passwordLabel) as HTMLInputElement).value).toBe("");
    expect(go).not.toHaveBeenCalled();
  });

  it("an account with no password is told so and offered no button", async () => {
    stubFetchRoutes({ "/api/auth/apple-link": status({ has_password: false }) });
    renderCard();
    expect(await screen.findByText(copy.needsPasswordTitle)).toBeTruthy();
    expect(screen.getByText(copy.needsPasswordBody)).toBeTruthy();
    expect(screen.queryByRole("button", { name: copy.connect })).toBeNull();
  });

  it("an account connected to Apple with no password cannot disconnect from here", async () => {
    stubFetchRoutes({ "/api/auth/apple-link": status({ linked: true, has_password: false }) });
    renderCard();
    expect(await screen.findByText(copy.connected)).toBeTruthy();
    expect(screen.getByText(copy.cannotDisconnect)).toBeTruthy();
    // The button is there, but disabled: the line says why.
    expect((screen.getByRole("button", { name: copy.disconnect }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("disconnects: confirms with the password, posts it, and says it is done", async () => {
    const fetchMock = stubFetchRoutes({
      "/api/auth/apple-link/unlink": { body: { unlinked: true } },
      "/api/auth/apple-link": status({ linked: true }),
    });
    renderCard();
    expect(await screen.findByText(copy.connected)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: copy.disconnect }));
    expect(screen.getByText(copy.disconnectTitle)).toBeTruthy();
    expect(screen.getByText(copy.disconnectBody)).toBeTruthy();
    typePassword("right");
    fireEvent.click(screen.getByRole("button", { name: copy.confirmDisconnect }));
    expect(await screen.findByText(copy.disconnected)).toBeTruthy();
    expect(JSON.parse(String((posted(fetchMock, "/api/auth/apple-link/unlink")[0][1] as RequestInit).body))).toEqual({ password: "right" });
  });

  it("Keep it closes the disconnect confirm without posting anything", async () => {
    const fetchMock = stubFetchRoutes({ "/api/auth/apple-link": status({ linked: true }) });
    renderCard();
    fireEvent.click(await screen.findByRole("button", { name: copy.disconnect }));
    fireEvent.click(screen.getByRole("button", { name: copy.keep }));
    expect(screen.queryByText(copy.disconnectTitle)).toBeNull();
    expect(posted(fetchMock, "/api/auth/apple-link/unlink")).toHaveLength(0);
  });

  it("shows what Apple sent the person back with, and not an unknown code", async () => {
    stubFetchRoutes({ "/api/auth/apple-link": status() });
    renderCard(vi.fn(), "/settings?apple=apple_in_use");
    expect(await screen.findByText(copy.returned.apple_in_use)).toBeTruthy();
    cleanup();
    stubFetchRoutes({ "/api/auth/apple-link": status({ linked: true }) });
    renderCard(vi.fn(), "/settings?apple=linked");
    expect(await screen.findByText(copy.returned.linked)).toBeTruthy();
    cleanup();
    stubFetchRoutes({ "/api/auth/apple-link": status() });
    renderCard(vi.fn(), "/settings?apple=nonsense");
    await screen.findByText(copy.connectLine);
    expect(screen.queryByRole("status")).toBeNull();
  });
});
