import { afterEach, describe, expect, it } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { ExportCard } from "./SettingsPage";

describe("ExportCard", () => {
  afterEach(() => {
    cleanup();
    window.sessionStorage.clear();
  });

  it("links to /api/export as a download", () => {
    render(<ExportCard />);
    const a = screen.getByRole("link", { name: "Export my data" });
    expect(a).toHaveAttribute("href", "/api/export");
    expect(a).toHaveAttribute("download");
    expect(screen.getByText(/Raw telemetry is in Backup/)).toBeInTheDocument();
  });

  it("is hidden while impersonating", () => {
    window.sessionStorage.setItem("rivolt.impersonate", JSON.stringify({ id: "u2", username: "someone" }));
    render(<ExportCard />);
    expect(screen.queryByRole("link", { name: "Export my data" })).toBeNull();
  });
});
