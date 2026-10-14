// Applies the light theme before first paint, only for users whose
// money_tab_enabled shell was on last time (cached by src/lib/theme.ts).
// Anything else, including blocked storage, stays dark. Kept as a static
// same-origin file because the CSP forbids inline scripts.
try {
  if (
    localStorage.getItem("rivolt.money_tab_enabled") === "1" &&
    window.matchMedia("(prefers-color-scheme: light)").matches
  ) {
    document.documentElement.setAttribute("data-theme", "light");
    document.querySelector('meta[name="theme-color"]').setAttribute("content", "#fafafa");
  }
} catch (e) {}
