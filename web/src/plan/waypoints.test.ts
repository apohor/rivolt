import { describe, expect, it } from "vitest";
import { stopsBucket } from "./waypoints";

describe("plan_run stops bucket", () => {
  it("groups the charging stops of a plan into three buckets", () => {
    expect([0, 1, 2, 3, 7].map(stopsBucket)).toEqual(["0", "1_2", "1_2", "3_plus", "3_plus"]);
  });

  it("is always a token the events allowlist accepts", () => {
    for (const n of [-1, 0, 1, 2, 3, 40]) expect(stopsBucket(n)).toMatch(/^[a-z0-9_.-]{1,40}$/);
  });
});
