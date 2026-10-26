import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ProviderButton, isApple } from "./ProviderButton";

const p = (name: string, display_name: string) => ({ name, display_name, start_url: `/api/auth/oidc/${name}/start` });

describe("ProviderButton", () => {
  it("uses Apple's wording and logo for apple", () => {
    const click = vi.fn();
    const { container } = render(<ProviderButton provider={p("apple", "Apple")} onClick={click} />);
    const b = screen.getByRole("button", { name: "Continue with Apple" });
    expect(container.querySelector("svg[aria-hidden='true']")).not.toBeNull();
    fireEvent.click(b);
    expect(click).toHaveBeenCalledOnce();
  });
  it("keeps the generic button for other providers", () => {
    const { container } = render(<ProviderButton provider={p("github", "GitHub")} onClick={() => {}} />);
    expect(screen.getByRole("button", { name: "Continue with GitHub" })).toBeTruthy();
    expect(container.querySelector("svg")).toBeNull();
  });
  it("matches the name case-insensitively", () => {
    expect(isApple({ name: "Apple" })).toBe(true);
    expect(isApple({ name: "google" })).toBe(false);
  });
});
