import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { WeatherCredit } from "./WeatherCredit";

describe("WeatherCredit", () => {
  it("links to open-meteo.com in the wording the licence asks for", () => {
    render(<WeatherCredit />);
    const a = screen.getByRole("link", { name: "Weather data by Open-Meteo.com" });
    expect(a.getAttribute("href")).toBe("https://open-meteo.com/");
    expect(a.getAttribute("rel")).toContain("noopener");
  });
});
