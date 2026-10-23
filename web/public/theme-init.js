// Applies the chosen theme before first paint, only on devices that have
// shown the signed-in app before (marked by src/lib/theme.ts rememberAppSeen),
// and on the public /demo, /privacy, /terms, /lease-calculator and /charging-cost (public pages that follow the device theme).
// rivolt.theme is system (default) | light | dark. Anything else,
// including blocked storage, stays dark. Kept as a static same-origin
// file because the CSP forbids inline scripts.
try {
  var onDemo = location.pathname === "/demo" || location.pathname.indexOf("/demo/") === 0 || location.pathname === "/privacy" || location.pathname === "/terms" || location.pathname === "/lease-calculator" || location.pathname === "/charging-cost";
  if (onDemo || localStorage.getItem("rivolt.money_tab_enabled") === "1") {
    var choice = localStorage.getItem("rivolt.theme");
    var light =
      choice === "light" ||
      (choice !== "dark" && window.matchMedia("(prefers-color-scheme: light)").matches);
    if (light) {
      document.documentElement.setAttribute("data-theme", "light");
      document.querySelector('meta[name="theme-color"]').setAttribute("content", "#fafafa");
    }
  }
} catch (e) {}
