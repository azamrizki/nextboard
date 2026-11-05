// Sets the theme before the first paint, so a light page never flashes dark. A separate file
// because the hub's CSP allows no inline script. lib/theme.ts takes over once the app loads.
(function () {
  var pref = null;
  try {
    pref = localStorage.getItem('nb.theme');
  } catch (e) {
    /* storage blocked: follow the system */
  }
  var light = pref === 'light' || (pref !== 'dark' && window.matchMedia && matchMedia('(prefers-color-scheme: light)').matches);
  document.documentElement.setAttribute('data-theme', light ? 'light' : 'dark');
  var meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute('content', light ? '#fafafa' : '#0a0a0a');
})();
