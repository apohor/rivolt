import { describe, expect, it } from "vitest";
import type { StatePrice } from "./api";
import { parseRate, placeLabel, priceCitation, priceDefault, rateText, truckPosition } from "./setupSheet";

const price: StatePrice = {
  state: "CA",
  name: "California",
  price_per_kwh: 0.3258,
  currency: "USD",
  source: { name: "EIA", url: "https://example.test/eia", period: "January to August 2026", released: "2026-10-23", checked: "2026-11-13" },
};

describe("setup sheet helpers", () => {
  it("takes a truck position only when it is a real fix", () => {
    expect(truckPosition({ latitude: 34.161, longitude: -118.168 })).toEqual({ lat: 34.161, lon: -118.168 });
    expect(truckPosition({ latitude: 0, longitude: 0 })).toBeNull();
    expect(truckPosition({ latitude: 95, longitude: 10 })).toBeNull();
    expect(truckPosition({ latitude: NaN, longitude: 10 })).toBeNull();
    expect(truckPosition(null)).toBeNull();
  });

  it("offers the state average only for a US place with a table row", () => {
    expect(priceDefault({ name: "x", country_code: "US", admin1: "California" }, price)).toBe(price);
    expect(priceDefault({ name: "x", country_code: "us", admin1: "California" }, price)).toBe(price);
    expect(priceDefault({ name: "x", country_code: "DE", admin1: "Bayern" }, price)).toBeNull();
    expect(priceDefault({ name: "x", country_code: "US", admin1: "Nowhere" }, null)).toBeNull();
    expect(priceDefault(null, price)).toBeNull();
  });

  it("labels the place and cites the table", () => {
    expect(placeLabel({ name: "Rose Bowl Drive, Pasadena", country_code: "US", admin1: "California" })).toBe("Rose Bowl Drive, Pasadena, California");
    expect(placeLabel({ name: "", country_code: "US", admin1: "California" })).toBe("California");
    expect(placeLabel(undefined)).toBe("");
    expect(priceCitation(price)).toBe("January to August 2026 average, residential (EIA, released 2026-10-23)");
  });

  it("formats and parses rates", () => {
    expect(rateText(0.33)).toBe("$0.33/kWh");
    expect(rateText(0.3258)).toBe("$0.326/kWh");
    expect(parseRate("0.14")).toBe(0.14);
    expect(parseRate("$0,14")).toBe(0.14);
    expect(parseRate("0")).toBeNull();
    expect(parseRate("abc")).toBeNull();
    expect(parseRate("14")).toBeNull();
  });
});
