// Applies the chosen theme before first paint, only for users whose
// money_tab_enabled shell was on last time (cached by src/lib/theme.ts),
// and on the public /demo (it showcases the themed app).
// rivolt.theme is system (default) | light | dark. Anything else,
// including blocked storage, stays dark. Kept as a static same-origin
// file because the CSP forbids inline scripts.
try {
  var onDemo = location.pathname === "/demo" || location.pathname.indexOf("/demo/") === 0;
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
