import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ImpersonationBanner } from "./ImpersonationBanner";
import { startImpersonation } from "../lib/impersonation";

describe("ImpersonationBanner", () => {
  beforeEach(() => {
    sessionStorage.clear();
    vi.stubGlobal("fetch", vi.fn(async () => new Response(null, { status: 204 })));
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("renders nothing when nobody is being viewed", () => {
    const { container } = render(<ImpersonationBanner />);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows who is being viewed with an Exit that stops the view", () => {
    startImpersonation({ id: "u-1", email: "demo@example.com" });
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    render(<ImpersonationBanner />);
    expect(screen.getByText(/Viewing as/)).toHaveTextContent("demo@example.com");
    fireEvent.click(screen.getByRole("button", { name: "Exit" }));
    expect(sessionStorage.getItem("rivolt.impersonate")).toBeNull();
    expect(assign).toHaveBeenCalledWith("/admin");
  });
});
