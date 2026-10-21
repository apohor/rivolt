import { describe, expect, it } from "vitest";
import { isCampgroundOnly } from "./poi";

describe("isCampgroundOnly: nearest charger must skip campground features", () => {
  it("skips a Recreation.gov campground with a hookup and no charger", () => {
    expect(isCampgroundOnly({ facility_type: "CAMPGROUND", hookup_amps: 50, ridb_id: "ridb/232447", "socket:nema_1450": "yes" })).toBe(true);
    expect(isCampgroundOnly({ hookup_amps: 30 })).toBe(true);
    expect(isCampgroundOnly({ facility_type: "rv_park" })).toBe(true);
    expect(isCampgroundOnly({ ridb_id: "ridb/1" })).toBe(true);
  });
  it("keeps real chargers, including a campground that has one", () => {
    expect(isCampgroundOnly({ facility_type: "HOTEL", l2_count: 2 })).toBe(false);
    expect(isCampgroundOnly({ network: "Electrify America", dcfc_count: 4 })).toBe(false);
    expect(isCampgroundOnly({ "socket:type1_combo": "yes" })).toBe(false);
    expect(isCampgroundOnly({ facility_type: "CAMPGROUND", hookup_amps: 50, l2_count: 1 })).toBe(false);
    expect(isCampgroundOnly({ facility_type: "CAMPGROUND", hookup_amps: 50, "socket:type1": "yes" })).toBe(false);
  });
});
