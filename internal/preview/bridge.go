package preview

import (
	"encoding/json"
	"net/http"
	"strings"
)

// serveBridge returns the injected bridge (docs/API-studio.md §4.5) with
// the hub origins it may talk to.
func (p *preview) serveBridge(w http.ResponseWriter, r *http.Request) {
	var origins []string
	if p.m.cfg.HubOrigins != nil {
		origins = p.m.cfg.HubOrigins(SchemeOf(r), r.Host)
	}
	if origins == nil {
		origins = []string{}
	}
	b, _ := json.Marshal(origins)
	prefix := ""
	if p.site != nil {
		prefix = p.site.cookiePrefix
	}
	cp, _ := json.Marshal(prefix)
	h := w.Header()
	h.Set("Content-Type", "application/javascript; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	js := strings.Replace(bridgeJS, "__NB_HUB_ORIGINS__", string(b), 1)
	_, _ = w.Write([]byte(strings.Replace(js, "__NB_COOKIE_PREFIX__", string(cp), 1)))
}

// bridgeJS speaks the postMessage protocol with the hub page that frames
// the preview: it only answers the origin that said hello (and only if it
// is a hub origin), reports navigation, console, errors and network calls,
// and beacons log batches to /__nb/log for the log card. On a site,
// document.cookie shows the page its own cookies by their own names, and what
// another site left in the browser on this port is gone before the page runs.
const bridgeJS = `(function () {
  "use strict";
  if (window.__nbBridge) return;
  window.__nbBridge = true;
  var HUB = __NB_HUB_ORIGINS__;
  var COOKIES = __NB_COOKIE_PREFIX__;
  var jar = COOKIES && Object.getOwnPropertyDescriptor(Document.prototype, "cookie");
  if (jar) Object.defineProperty(document, "cookie", {
    configurable: true,
    get: function () {
      return jar.get.call(document).split("; ").filter(function (c) { return c.indexOf(COOKIES) === 0; })
        .map(function (c) { return c.slice(COOKIES.length); }).join("; ");
    },
    set: function (v) {
      var c = String(v).replace(/^\s+/, "").replace(/;\s*domain=[^;]*/i, "");
      if (location.protocol !== "https:") c = c.replace(/;\s*secure\s*(?=;|$)/i, "").replace(/samesite=none/i, "SameSite=Lax");
      jar.set.call(document, COOKIES + c);
    }
  });
  if (jar) {
    // Origins go by port, and a port serves another site once its last one is gone. The marker is
    // a cookie because a page may clear its own storage: that must not look like a new owner.
    var mark = "nb_pv_owner_" + location.port;
    var owner = jar.get.call(document).split("; ").indexOf(mark + "=" + COOKIES) >= 0;
    if (!owner) {
      try {
        localStorage.clear();
        sessionStorage.clear();
        if (window.caches) caches.keys().then(function (ks) { ks.forEach(function (k) { caches.delete(k); }); });
        if (indexedDB.databases) indexedDB.databases().then(function (ds) { ds.forEach(function (d) { indexedDB.deleteDatabase(d.name); }); });
      } catch (e) { /* storage may be off */ }
      jar.set.call(document, mark + "=" + COOKIES + "; path=/; max-age=31536000; SameSite=Lax" + (location.protocol === "https:" ? "; Secure" : ""));
    }
  }
  var parentOrigin = null, pending = [], logs = [], timer = null, sending = false;
  var top = window.parent !== window;
  function post(m) {
    m.nb = "bridge"; m.v = 1;
    if (!top) return;
    if (!parentOrigin) { if (pending.length < 200) pending.push(m); return; }
    try { window.parent.postMessage(m, parentOrigin); } catch (e) {}
  }
  function str(v) {
    var s;
    try {
      if (typeof v === "string") s = v;
      else if (v instanceof Error) s = (v.stack || (v.name + ": " + v.message));
      else s = JSON.stringify(v);
      if (s === undefined) s = String(v);
    } catch (e) { s = String(v); }
    return s.length > 2048 ? s.slice(0, 2048) + "…" : s;
  }
  function here() { return location.href; }
  function beacon(e) {
    e.page = here();
    logs.push(e);
    if (logs.length > 300) logs.splice(0, logs.length - 300);
    if (!timer) timer = setTimeout(flushLogs, 400);
  }
  function flushLogs() {
    timer = null;
    if (!logs.length || sending) { if (logs.length) timer = setTimeout(flushLogs, 400); return; }
    var batch = logs.splice(0, 100), body = JSON.stringify({ entries: batch });
    while (body.length > 60000 && batch.length > 1) { logs.unshift(batch.pop()); body = JSON.stringify({ entries: batch }); }
    sending = true;
    try {
      origFetch.call(window, "/__nb/log", { method: "POST", body: body, keepalive: body.length < 60000,
        headers: { "Content-Type": "application/json" }, credentials: "same-origin" })
        .then(function () { sending = false; if (logs.length) timer = setTimeout(flushLogs, 120); },
              function () { sending = false; });
    } catch (e) { sending = false; }
  }
  window.addEventListener("message", function (ev) {
    var d = ev.data;
    if (!d || d.nb !== "bridge" || ev.source !== window.parent) return;
    if (HUB.indexOf(ev.origin) < 0) return;
    if (d.type === "hello") {
      parentOrigin = ev.origin;
      post({ type: "ready", url: here(), title: document.title });
      var q = pending; pending = [];
      q.forEach(function (m) { try { window.parent.postMessage(m, parentOrigin); } catch (e) {} });
      return;
    }
    if (ev.origin !== parentOrigin) return;
    switch (d.type) {
      case "back": history.back(); break;
      case "forward": history.forward(); break;
      case "reload":
        // A hard reload revalidates the document first (no URL change).
        if (d.hard && origFetch) origFetch.call(window, location.href, { cache: "reload", credentials: "same-origin" })
          .then(function () { location.reload(); }, function () { location.reload(); });
        else location.reload();
        break;
      case "navigate":
        try {
          var t = new URL(String(d.url || "/"), location.href);
          if (t.origin === location.origin) location.assign(t.pathname + t.search + t.hash);
        } catch (e) {}
        break;
      case "fill":
        post(Object.assign({ type: "filled", id: d.id }, d.fields ? fillFields(d.fields) : fillForm(d)));
        break;
    }
  });
  // Filling a login, only ever when the person picked one in the hub's page (docs/API-vault.md).
  // Nothing is stored or reported but which kinds of field got a value. A field nobody can see
  // gets nothing: that is how a page would collect more than it shows.
  function shown(el) {
    if (el.disabled || el.readOnly) return false;
    var r = el.getBoundingClientRect();
    if (r.width < 4 || r.height < 4 || r.right <= 0 || r.left >= window.innerWidth || r.bottom + window.scrollY <= 0) return false;
    if (getComputedStyle(el).visibility !== "visible") return false;
    for (var e = el, up = 0; e && e.nodeType === 1 && up < 12; e = e.parentElement, up++) {
      if (parseFloat(getComputedStyle(e).opacity) < 0.1) return false;
    }
    return true;
  }
  function visibleInputs(selects) {
    var out = [];
    (function walk(root, depth) {
      var all = root.querySelectorAll("*");
      for (var i = 0; i < all.length && out.length < 200; i++) {
        if ((all[i].tagName === "INPUT" || (selects && all[i].tagName === "SELECT")) && shown(all[i])) out.push(all[i]);
        if (all[i].shadowRoot && depth < 8) walk(all[i].shadowRoot, depth + 1);
      }
    })(document, 0);
    return out;
  }
  var USER_HINT = /user|login|e-?mail|account|identifier|phone/i, CODE_HINT = /otp|2fa|mfa|one.?time|verif|code|token/i;
  function hints(el) {
    var label = el.labels && el.labels[0] ? el.labels[0].textContent : "";
    return [el.name, el.id, el.autocomplete, el.placeholder, el.getAttribute("aria-label"), label].join(" ");
  }
  function isText(el) { return /^(text|email|tel|url|number|)$/.test(el.getAttribute("type") ? el.type : ""); }
  function setValue(el, v) {
    // Past whatever a framework wrapped around the value, so that it sees this as typing.
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true, composed: true }));
    el.dispatchEvent(new Event("change", { bubbles: true, composed: true }));
  }
  function fillCode(list, code) {
    var boxes = list.filter(function (el) { return isText(el) && el.maxLength === 1; });
    if (boxes.length >= code.length) {
      for (var i = 0; i < code.length; i++) { boxes[i].focus(); setValue(boxes[i], code[i]); }
      return true;
    }
    var one = list.filter(function (el) { return el.autocomplete === "one-time-code"; })[0] ||
      list.filter(function (el) { return isText(el) && CODE_HINT.test(hints(el)); })[0];
    if (!one) return false;
    one.focus();
    setValue(one, code);
    return true;
  }
  // A card or an identity: values by autocomplete token. A field says which it wants with
  // autocomplete, or failing that by its name, id, placeholder or label.
  var FIELD_HINTS = {
    "cc-name": /card.?holder|name.?on.?card|cc.?name/i,
    "cc-number": /card.?n(umber|o\b)|cc.?num|\bpan\b/i,
    "cc-csc": /cvv|cvc|csc|security.?code|card.?code|\bcid\b/i,
    "cc-exp-month": /exp\w*.?m(onth|m)\b|cc.?month/i,
    "cc-exp-year": /exp\w*.?y(ear|y)\b|cc.?year/i,
    "cc-exp": /expir|exp.?date|mm.?yy|cc.?exp/i,
    "cc-type": /card.?(type|brand)|cc.?type/i,
    "given-name": /first.?name|given.?name|\bfname\b/i,
    "additional-name": /middle.?name|\bmname\b/i,
    "family-name": /last.?name|family.?name|surname|\blname\b/i,
    "name": /full.?name|your.?name|^\s*name\b/i,
    "organization": /company|organi[sz]ation|business/i,
    "email": /e-?mail/i,
    "tel": /phone|\btel\b|mobile/i,
    "address-line1": /address.?(line)?.?1|street|\baddr1\b|^\s*address\b/i,
    "address-line2": /address.?(line)?.?2|\baddr2\b|apartment|suite/i,
    "address-line3": /address.?(line)?.?3|\baddr3\b/i,
    "address-level2": /city|town|locality/i,
    "address-level1": /state|province|region|county/i,
    "postal-code": /zip|postal|post.?code/i,
    "country-name": /country/i,
    "username": /user.?name|login/i
  };
  function tokens(el) { return String(el.getAttribute("autocomplete") || "").toLowerCase().split(/\s+/); }
  function setField(el, v) {
    if (el.tagName !== "SELECT") { el.focus(); setValue(el, v); return true; }
    var want = String(v).toLowerCase(), n = Number(v), opts = el.options;
    for (var i = 0; i < opts.length; i++) {
      var o = opts[i], val = String(o.value).toLowerCase(), text = String(o.text).trim().toLowerCase();
      if (val === want || text === want || (!isNaN(n) && n > 0 && (Number(o.value) === n || Number(o.text) === n)) ||
        (want.length === 2 && !isNaN(n) && (val.slice(-2) === want || text.slice(-2) === want) && /^\d{4}$/.test(val || text))) {
        Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value").set.call(el, o.value);
        el.dispatchEvent(new Event("input", { bubbles: true, composed: true }));
        el.dispatchEvent(new Event("change", { bubbles: true, composed: true }));
        return true;
      }
    }
    return false;
  }
  function fillFields(values) {
    var list = visibleInputs(true).filter(function (el) { return el.type !== "password" && el.type !== "hidden"; });
    var used = [], got = {}, keys = Object.keys(values).filter(function (k) { return FIELD_HINTS[k] && values[k]; });
    function take(k, el) { if (used.indexOf(el) < 0 && setField(el, String(values[k]))) { used.push(el); got[k] = true; } }
    // first what the page says by autocomplete ("country" stands for its name too)
    keys.forEach(function (k) {
      list.forEach(function (el) {
        var t = tokens(el);
        if (!got[k] && (t.indexOf(k) >= 0 || (k === "country-name" && t.indexOf("country") >= 0))) take(k, el);
      });
    });
    keys.forEach(function (k) {
      if (got[k]) return;
      // one expiry field only where there are no month and year ones; one name field only
      // where there are no first and last ones
      if (k === "cc-exp" && (got["cc-exp-month"] || got["cc-exp-year"])) return;
      if (k === "name" && (got["given-name"] || got["family-name"])) return;
      for (var i = 0; i < list.length && !got[k]; i++) {
        var el = list[i];
        if (used.indexOf(el) >= 0 || tokens(el).some(function (t) { return FIELD_HINTS[t]; })) continue;
        if (FIELD_HINTS[k].test(hints(el))) take(k, el);
      }
    });
    return { username: false, password: false, code: false, fields: Object.keys(got).length };
  }
  function fillForm(d) {
    var done = { username: false, password: false, code: false };
    var list = visibleInputs();
    var pass = list.filter(function (el) { return el.type === "password"; })[0];
    if (d.password && pass) { pass.focus(); setValue(pass, String(d.password)); done.password = true; }
    if (d.username) {
      var user = null;
      if (pass) {
        // The nearest text field before the password, in its form if it has one.
        var at = list.indexOf(pass);
        for (var i = at - 1; i >= 0 && !user; i--) {
          if (isText(list[i]) && !CODE_HINT.test(hints(list[i])) && (!pass.form || list[i].form === pass.form)) user = list[i];
        }
      }
      if (!user) user = list.filter(function (el) { return isText(el) && USER_HINT.test(hints(el)); })[0] || null;
      if (user) { user.focus(); setValue(user, String(d.username)); done.username = true; }
    }
    if (d.code) done.code = fillCode(list, String(d.code));
    if (pass && done.password) pass.focus();
    return done;
  }
  function nav() { post({ type: "nav", url: here(), title: document.title }); }
  ["pushState", "replaceState"].forEach(function (k) {
    var o = history[k];
    if (typeof o !== "function") return;
    history[k] = function () { var r = o.apply(this, arguments); setTimeout(nav, 0); return r; };
  });
  window.addEventListener("popstate", nav);
  window.addEventListener("hashchange", nav);
  window.addEventListener("load", function () { post({ type: "ready", url: here(), title: document.title }); });
  ["log", "info", "warn", "error", "debug"].forEach(function (lv) {
    var o = console[lv];
    if (typeof o !== "function") return;
    console[lv] = function () {
      var args = Array.prototype.slice.call(arguments, 0, 20).map(str), at = new Date().toISOString();
      post({ type: "console", level: lv, args: args, at: at });
      beacon({ type: "console", level: lv, args: args });
      return o.apply(this, arguments);
    };
  });
  function errorOut(m) { post(Object.assign({ type: "error" }, m)); beacon(Object.assign({ type: "error" }, m)); }
  window.addEventListener("error", function (e) {
    if (e && e.message) errorOut({ message: str(e.message), source: str(e.filename || ""), line: e.lineno || 0, col: e.colno || 0,
      stack: e.error && e.error.stack ? str(e.error.stack) : "" });
  });
  window.addEventListener("unhandledrejection", function (e) {
    var r = e && e.reason;
    errorOut({ message: "Unhandled rejection: " + str(r && r.message ? r.message : r), stack: r && r.stack ? str(r.stack) : "" });
  });
  function netOut(method, url, status, t0) {
    var u = String(url || "");
    if (u.indexOf("/__nb/") >= 0) return;
    var m = { type: "net", method: String(method || "GET").toUpperCase(), url: u.slice(0, 1024), status: status | 0, ms: Math.round(performance.now() - t0) };
    post(m); beacon(m);
  }
  var origFetch = window.fetch;
  if (typeof origFetch === "function") {
    window.fetch = function (input, init) {
      var t0 = performance.now(), method = (init && init.method) || (input && input.method) || "GET",
          url = typeof input === "string" ? input : (input && input.url) || String(input);
      return origFetch.apply(this, arguments).then(function (r) { netOut(method, url, r.status, t0); return r; },
        function (err) { netOut(method, url, 0, t0); throw err; });
    };
  }
  var X = window.XMLHttpRequest;
  if (X && X.prototype) {
    var oOpen = X.prototype.open, oSend = X.prototype.send;
    X.prototype.open = function (m, u) { this.__nb = { m: m, u: u }; return oOpen.apply(this, arguments); };
    X.prototype.send = function () {
      var x = this, t0 = performance.now();
      if (x.__nb) x.addEventListener("loadend", function () { netOut(x.__nb.m, x.__nb.u, x.status, t0); });
      return oSend.apply(this, arguments);
    };
  }
  var oOpenWin = window.open;
  window.open = function (u) {
    if (!top) return oOpenWin.apply(window, arguments);
    try { var t = new URL(String(u || ""), location.href); post({ type: "open", url: t.toString() }); } catch (e) {}
    return null;
  };
  document.addEventListener("click", function (e) {
    if (!top || e.defaultPrevented) return;
    var a = e.target && e.target.closest ? e.target.closest("a[target=_blank]") : null;
    if (!a || !a.href) return;
    e.preventDefault();
    post({ type: "open", url: a.href });
  }, true);
})();
`
