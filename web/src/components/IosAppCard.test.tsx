import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { IosAppCard } from "./IosAppCard";
import * as config from "../lib/config";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("Settings → Get the iPhone app", () => {
  it("shows nothing until the server reports an app link", () => {
    vi.spyOn(config, "useIosAppUrl").mockReturnValue("");
    const { container } = render(<IosAppCard />);
    expect(container).toBeEmptyDOMElement();
  });
  it("links an App Store listing directly", () => {
    vi.spyOn(config, "useIosAppUrl").mockReturnValue("https://apps.apple.com/us/app/rivolt/id1234567890");
    render(<IosAppCard />);
    expect(screen.getByText("Get the iPhone app")).toBeInTheDocument();
    const a = screen.getByRole("link", { name: "Open in the App Store" });
    expect(a).toHaveAttribute("href", "https://apps.apple.com/us/app/rivolt/id1234567890");
    expect(a).toHaveAttribute("rel", expect.stringContaining("noopener"));
    expect(screen.getByText(/never sees your Rivian password/)).toBeInTheDocument();
  });
  it("says Join the TestFlight beta for a TestFlight link, and refuses a look-alike", () => {
    vi.spyOn(config, "useIosAppUrl").mockReturnValue("https://testflight.apple.com/join/abc123");
    render(<IosAppCard />);
    expect(screen.getByRole("link", { name: "Join the TestFlight beta" })).toBeInTheDocument();
    cleanup();
    vi.spyOn(config, "useIosAppUrl").mockReturnValue("https://evil.example/apps.apple.com");
    const { container } = render(<IosAppCard />);
    expect(container).toBeEmptyDOMElement();
  });
});
