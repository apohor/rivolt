import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render } from "@testing-library/react";
import { useSystemTheme, writeThemeChoice } from "./theme";

function Probe({ enabled }: { enabled: boolean }) {
  useSystemTheme(enabled);
  return null;
}

// A controllable prefers-color-scheme: light.
function mockOS(initialLight: boolean) {
  let light = initialLight;
  const listeners = new Set<() => void>();
  vi.stubGlobal("matchMedia", () => ({
    get matches() {
      return light;
    },
    addEventListener: (_: string, fn: () => void) => listeners.add(fn),
    removeEventListener: (_: string, fn: () => void) => listeners.delete(fn),
  }));
  return {
    set(v: boolean) {
      light = v;
      listeners.forEach((f) => f());
    },
    listeners,
  };
}

const theme = () => document.documentElement.getAttribute("data-theme");

describe("useSystemTheme", () => {
  beforeEach(() => {
    window.localStorage.clear();
    document.documentElement.removeAttribute("data-theme");
  });
  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("System follows the OS, including live changes", () => {
    const os = mockOS(false);
    render(<Probe enabled />);
    expect(theme()).toBeNull();
    act(() => os.set(true));
    expect(theme()).toBe("light");
    act(() => os.set(false));
    expect(theme()).toBeNull();
  });

  it("Dark and Light ignore the OS", () => {
    const os = mockOS(true);
    render(<Probe enabled />);
    expect(theme()).toBe("light");
    act(() => writeThemeChoice("dark"));
    expect(theme()).toBeNull();
    act(() => os.set(false));
    act(() => os.set(true));
    expect(theme()).toBeNull(); // forced dark under OS light
    act(() => writeThemeChoice("light"));
    expect(theme()).toBe("light");
    expect(os.listeners.size).toBe(0); // not following the OS any more
  });

  it("does nothing with the shell off", () => {
    mockOS(true);
    render(<Probe enabled={false} />);
    expect(theme()).toBeNull();
  });
});
