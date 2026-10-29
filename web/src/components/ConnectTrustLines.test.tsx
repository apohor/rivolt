import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { ConnectTrustLines, CONNECT_TRUST_LINE } from "./ConnectTrustLines";
import * as config from "../lib/config";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

const renderLines = () =>
  render(
    <MemoryRouter>
      <ConnectTrustLines />
    </MemoryRouter>,
  );

describe("the lines above the Rivian sign-in form", () => {
  it("say what Rivolt does with the login: reads, sends no commands, never stores the password", () => {
    vi.spyOn(config, "useDemoEnabled").mockReturnValue(false);
    renderLines();
    expect(screen.getByText(CONNECT_TRUST_LINE)).toBeInTheDocument();
    expect(CONNECT_TRUST_LINE).toMatch(/sends no commands to your truck/);
    // It names location, the most sensitive thing read, and says "such as" so the list is not read as a promise (DA).
    expect(CONNECT_TRUST_LINE).toMatch(/such as drives, charges, battery and location/);
    expect(CONNECT_TRUST_LINE).toBe(
      "Connect a Rivian account and Rivolt reads your truck's data, such as drives, charges, battery and location. It sends no commands to your truck, and your Rivian password is used once to sign in and never stored.",
    );
    expect(CONNECT_TRUST_LINE).toMatch(/password is used once to sign in and never stored/);
    // No longer points people at their main login.
    expect(document.body.textContent).not.toMatch(/same email and password you use in the Rivian app/);
  });
  it("offer the in-app sample truck when the install has one", () => {
    vi.spyOn(config, "useDemoEnabled").mockReturnValue(true);
    renderLines();
    expect(screen.getByRole("link", { name: "Look at a sample truck" })).toHaveAttribute("href", "/demo");
    expect(screen.getByText(/with made-up data first; no account needed\./)).toBeInTheDocument();
  });
  it("offer nothing where there is no demo to open", () => {
    vi.spyOn(config, "useDemoEnabled").mockReturnValue(false);
    renderLines();
    expect(screen.queryByRole("link", { name: "Look at a sample truck" })).not.toBeInTheDocument();
  });
});
