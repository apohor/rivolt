import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Spinner, ErrorBox } from "./ui";
import Logo from "./Logo";

// Render smoke tests: prove the React + jsdom toolchain mounts real
// components. A breaking React major bump surfaces here rather than
// only on preview.
describe("ui smoke", () => {
  it("renders the Spinner with a status role", () => {
    render(<Spinner />);
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("renders ErrorBox title and optional detail", () => {
    render(<ErrorBox title="Boom" detail="something broke" />);
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Boom");
    expect(alert).toHaveTextContent("something broke");
  });

  it("renders the Logo as an svg at the requested size", () => {
    const { container } = render(<Logo size={48} />);
    const svg = container.querySelector("svg");
    expect(svg).not.toBeNull();
    expect(svg).toHaveAttribute("width", "48");
  });
});
