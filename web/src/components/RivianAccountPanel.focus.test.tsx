import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { stubFetchRoutes } from "../test/fetchRoutes";

// signup_v2 card focus: mounting it on a page the reader is on (Now, Settings) must not take focus; onboarding does; a step the user takes does.
async function mount(props: { focusOnMount?: boolean }) {
  vi.resetModules();
  stubFetchRoutes({
    "/api/config": { body: { features: { design_preview: ["signup_v2"] } } },
    "/api/settings/rivian": { body: { enabled: true, authenticated: false, mfa_pending: false } },
    "/api/settings/rivian/login": { body: { authenticated: false, mfa_pending: true } },
    "/api/events": { body: {} },
  });
  const { RivianAccountPanel } = await import("./RivianAccountPanel");
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <RivianAccountPanel v2 {...props} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  await screen.findByRole("button", { name: "Sign in to Rivian" });
}

describe("connect card focus", () => {
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("inline (Now, Settings): the card appearing leaves focus where it was", async () => {
    const reader = document.createElement("button");
    document.body.appendChild(reader);
    reader.focus();
    await mount({});
    expect(document.activeElement).toBe(reader);
    reader.remove();
  });

  it("onboarding: the card takes focus on its heading", async () => {
    await mount({ focusOnMount: true });
    await waitFor(() => expect(document.activeElement?.tagName).toBe("H2"));
  });

  it("a step the user takes moves focus to the new step", async () => {
    await mount({});
    fireEvent.change(screen.getByLabelText("Rivian email"), { target: { value: "a@b.c" } });
    fireEvent.change(screen.getByLabelText("Rivian password"), { target: { value: "pw" } });
    expect(document.activeElement?.tagName).not.toBe("H2");
    fireEvent.click(screen.getByRole("button", { name: "Sign in to Rivian" }));
    await screen.findByLabelText(/code/i);
    await waitFor(() => expect(document.activeElement?.tagName).toBe("H2"));
  });
});
