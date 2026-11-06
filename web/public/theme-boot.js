// Sets the theme and the layout before the first paint, so a light page never flashes dark and a
// phone never flashes the desktop header. A separate file because the hub's CSP allows no inline
// script. lib/theme.ts and lib/layout.ts take over once the app loads, with the same rules.
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
(function () {
  var pref = null;
  try {
    pref = localStorage.getItem('nb.layout');
  } catch (e) {
    /* storage blocked: decide from the screen */
  }
  var touch = !!(window.matchMedia && matchMedia('(pointer: coarse)').matches);
  var w = window.innerWidth;
  var short = Math.min(screen.width || w, screen.height || window.innerHeight);
  var layout =
    pref === 'phone' || pref === 'tablet' || pref === 'desktop' ? pref : w < 600 || (touch && short < 600) ? 'phone' : w < 1200 || touch ? 'tablet' : 'desktop';
  document.documentElement.setAttribute('data-layout', layout);
  document.documentElement.setAttribute('data-input', touch ? 'touch' : 'mouse');
})();
