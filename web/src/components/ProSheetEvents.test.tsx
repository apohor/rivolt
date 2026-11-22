import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import freeMe from "../../../ios/RivoltTests/Fixtures/me_plan_free.json";

const track = vi.fn();
vi.mock("../lib/track", () => ({ track: (...a: unknown[]) => track(...a) }));

import { ProLimitNotice } from "./ProLimitNotice";
import { ProSheet } from "./ProSheetLazy";
import { planState } from "../lib/plan";
import { PRO_FROM, proFromFeature } from "../lib/proFrom";

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(["auth", "me"], freeMe);
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("pro_sheet_open", () => {
  beforeEach(() => track.mockClear());
  afterEach(cleanup);

  it("is not sent while the sheet is closed, and once on the tap that opens it, with the entry point and nothing else", async () => {
    wrap(<ProLimitNotice limit={{ code: "pro_required", limit: 1 } as never} feature="saved-trips" />);
    await screen.findByRole("button", { name: /See Pro/ });
    expect(track).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: /See Pro/ }));
    await waitFor(() => expect(track).toHaveBeenCalledTimes(1));
    expect(track).toHaveBeenCalledWith("pro_sheet_open", { from: "saved_trips" });
  });

  it("sends the entry point it was given, once per opening", async () => {
    const { rerender } = wrap(<ProSheet from="truck" state={planState(freeMe)} onClose={() => {}} />);
    await waitFor(() => expect(track).toHaveBeenCalledWith("pro_sheet_open", { from: "truck" }));
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter>
          <ProSheet from="truck" state={planState(freeMe)} onClose={() => {}} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(track).toHaveBeenCalledTimes(1);
  });

  it("maps every limit notice to a value in the fixed list", () => {
    for (const f of [undefined, "planner", "plan-vs-actual", "saved-trips", "vehicle"] as const) {
      expect(PRO_FROM).toContain(proFromFeature(f));
    }
  });
});
