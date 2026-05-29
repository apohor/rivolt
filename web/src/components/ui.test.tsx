import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/dom";
import { Spinner, ErrorBox, clickableRowProps } from "./ui";
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

describe("clickableRowProps", () => {
  it("exposes the row as a focusable control with a label", () => {
    const onActivate = vi.fn();
    render(
      <table>
        <tbody>
          <tr {...clickableRowProps(onActivate, { role: "link", label: "Open row" })}>
            <td>cell</td>
          </tr>
        </tbody>
      </table>,
    );
    const row = screen.getByRole("link", { name: "Open row" });
    expect(row).toHaveAttribute("tabindex", "0");
  });

  it("activates on click, Enter, and Space", () => {
    const onActivate = vi.fn();
    render(
      <table>
        <tbody>
          <tr {...clickableRowProps(onActivate, { label: "Pick" })}>
            <td>cell</td>
          </tr>
        </tbody>
      </table>,
    );
    const row = screen.getByRole("button", { name: "Pick" });
    fireEvent.click(row);
    fireEvent.keyDown(row, { key: "Enter" });
    fireEvent.keyDown(row, { key: " " });
    expect(onActivate).toHaveBeenCalledTimes(3);
  });
});
