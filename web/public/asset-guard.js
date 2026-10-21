// Recovers a page that loaded across a deploy. During a rolling update the
// browser can get the new index.html from a new server and then ask an old
// one for the new /assets/ bundle (404), or a tab opened before a deploy can
// ask for a lazy chunk that no longer exists. Either way the app would sit
// blank or broken; a reload gets a consistent version.
//
// At most 2 reloads in 2 minutes per tab (sessionStorage), so a file that's
// really missing can't cause a reload loop; without storage, no reload at all.
// Loaded as a blocking same-origin script before the app's module entry,
// because the CSP forbids inline scripts. Plain ES5: it runs before, and
// without, the app's own polyfills.
(function () {
  var KEY = "rivolt.assetReloads";
  var MAX = 2;
  var WINDOW_MS = 2 * 60 * 1000;
  var DELAY_MS = 1500;
  var pending = false;

  // reload schedules one reload if the budget allows it; false if not.
  function reload() {
    if (pending) return true;
    var now = Date.now();
    var state;
    try {
      state = JSON.parse(sessionStorage.getItem(KEY) || "null");
      if (!state || typeof state.n !== "number" || now - state.first > WINDOW_MS) {
        state = { n: 0, first: now };
      }
      if (state.n >= MAX) return false;
      state.n += 1;
      sessionStorage.setItem(KEY, JSON.stringify(state));
    } catch (e) {
      return false;
    }
    pending = true;
    setTimeout(function () {
      location.reload();
    }, DELAY_MS);
    return true;
  }

  function isAsset(url) {
    return typeof url === "string" && url.indexOf("/assets/") !== -1;
  }

  // A <script> or stylesheet <link> under /assets/ that failed to load.
  // Resource errors don't bubble, hence the capture phase.
  window.addEventListener(
    "error",
    function (e) {
      var t = e.target;
      if (!t || t === window || !t.tagName) return;
      var tag = t.tagName.toUpperCase();
      var url = tag === "SCRIPT" ? t.src : tag === "LINK" && /stylesheet/i.test(t.rel) ? t.href : "";
      if (isAsset(url)) reload();
    },
    true
  );

  // Vite's event for a lazy chunk (dynamic import) that failed to load.
  // Only swallowed when a reload is on its way; otherwise the app's own
  // error handling shows what's wrong.
  window.addEventListener("vite:preloadError", function (e) {
    if (reload()) e.preventDefault();
  });
})();
