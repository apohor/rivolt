import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

const track = vi.fn();
vi.mock("../lib/track", () => ({ track: (...a: unknown[]) => track(...a) }));

import { CsvDownloadLink } from "./MoneyPage";

describe("Money CSV download", () => {
  afterEach(() => {
    cleanup();
    track.mockReset();
  });

  it("counts one statement_share{csv}, with no month or amounts", () => {
    render(<CsvDownloadLink month="2026-10" disabled={false} />);
    fireEvent.click(screen.getByText("Download CSV"));
    expect(track).toHaveBeenCalledTimes(1);
    expect(track).toHaveBeenCalledWith("statement_share", { kind: "csv" });
  });

  it("counts nothing when the link is disabled", () => {
    render(<CsvDownloadLink month="2026-10" disabled />);
    fireEvent.click(screen.getByText("Download CSV"));
    expect(track).not.toHaveBeenCalled();
  });
});
