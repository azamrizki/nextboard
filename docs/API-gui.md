# NextBoard — desktop apps on the board

Contract for the **desktop app tile** feature: GUI apps installed on This PC (Chrome, VS Code and others) show in a tile on the board and can be controlled from it. Other clients (Android, desktop) can follow with this contract; until then they show an `app` node as "Unsupported tile".

## 1. How it works

Each app session has a virtual display of its own:

```
tile (iframe viewer + noVNC) ⇄ hub WebSocket ⇄ Unix socket ⇄ wayvnc ⇄ sway headless ⇄ app          (§8)
tile (WebCodecs)             ⇠ hub WebSocket ⇠ wf-recorder AV1/H.264 (GPU) ⇠ sway headless     (large motion, §7)
tile (AudioContext)          ⇠ hub WebSocket ⇠ pw-record ⇠ session's PipeWire sink ⇠ app     (sound, §7)
```

- Apps **do not appear on the physical screen**. Input from the tile does not disturb the desktop session in use on This PC.
- An app has only **one session**, with **one NextBoard-only profile** in `<data>/gui/profiles/<appId>/`. Logins, extensions and history in the original profile (`~/.config/google-chrome`, `~/.config/Code`) are neither used nor touched.
- Sessions run detached from the hub: a hub restart or stop does not close the app, and the next hub picks it up again through `<data>/gui/sessions/<id>/state.json`. Under systemd, the processes move to a scope of their own (`systemd-run --user --scope`), because `KillMode=mixed` in the hub's unit would kill them too.
- A session ends when the last window is closed inside the app, the tile is deleted, the session is stopped through the API, or the display dies. Closing the workspace or project also ends it, unless the app still shows on another board.

**Requirements on This PC** (not hub dependencies; `GET /api/gui/apps` reports what is missing). One of two kinds of display:

```sh
# main path, needs a GPU and ffmpeg with VA-API (§8)
sudo apt install --no-install-recommends sway wayvnc wf-recorder xwayland
# fallback without a GPU
sudo apt install tigervnc-standalone-server openbox
```

Hardware video needs `ffmpeg` with `h264_vaapi` (and `av1_vaapi` for AV1) and a GPU the hub can open (`/dev/dri/renderD*`). Sound needs PipeWire (`pw-loopback`, `pw-record`). Without them the tile still works, only without those features. `NEXTBOARD_GUI_GPU=off` in the hub's environment turns off GPU use, `NEXTBOARD_GUI_BACKEND=x11` forces an X11 display.

Linux only, and This PC only. SSH machines are not supported, because the agentless rule forbids installing packages on the target server.

**Security.** Anyone with access to the hub can control these apps, the same as the terminal access that already exists.

- The VNC server listens only on a Unix socket in a `0700` folder, with no TCP port. That is why the RFB layer uses no password.
- The Wayland socket is in a `0700` folder per session. Xtigervnc uses a `MIT-MAGIC-COOKIE-1` cookie per session. Xwayland has no cookie, so its access list is locked to the hub's user before the app runs (§8).
- The WebSockets use the same authentication and origin check as the terminal.
- Chromium and VS Code profiles use `--password-store=basic`, because the private display has no keyring (a real keyring would show a prompt on the physical screen). Passwords and tokens saved in those profiles are protected only by the file permissions of `<data>`.

## 2. App catalog

The source is `.desktop` files in the XDG data directories (including flatpak and snap exports). Hidden: `NoDisplay`, `Hidden`, `Terminal=true`, entries with `OnlyShowIn` (desktop session settings panels), and programs not on `PATH`. `Exec` never runs through a shell.

| Method | Path | Response |
|---|---|---|
| GET | `/api/gui/apps` | `{ "available": bool, "missing": ["openbox"], "hint": "sudo apt install …", "gpu": "/dev/dri/renderD128", "video": bool, "audio": bool, "apps": [App…] }` |
| GET | `/api/gui/apps/{id}/icon` | PNG or SVG; `404 icon_not_found` when there is no file that can be served |

```jsonc
// App
{
  "id": "google-chrome",           // desktop file ID
  "name": "Google Chrome",
  "comment": "Access the Internet",
  "categories": ["Network", "WebBrowser"],
  "acceptsFolder": false           // true: a folder path may be sent at launch
}
```

`acceptsFolder` applies to the VS Code family and to apps that declare `inode/directory`, as long as their `Exec` accepts files.

## 3. Sessions

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/gui/sessions` | — | `[Session…]` |
| POST | `/api/gui/sessions` | `{ "appId", "path"?, "width"?, "height"? }` | `201 Session` |
| POST | `/api/gui/browser/open` | `{ "url", "width"?, "height"?, "here"?, "instance"? }` | `201` a new browser opened, `200` it became a new tab: `{ "session": Session, "url", "newTab": bool }`, see §8 |
| DELETE | `/api/gui/sessions/{id}` | — | `{ "ok": true }`; stopping runs in the background, see the events |
| GET | `/api/gui/sessions/{id}/ws` | WebSocket | RFB bridge, see §4 |
| GET | `/api/gui/sessions/{id}/video?w=&h=&mbps=&codec=` | WebSocket | AV1 or H.264, see §7 |
| GET | `/api/gui/sessions/{id}/audio` | WebSocket | PCM, see §7 |

```jsonc
// Session
{
  "id": "g_8dcf242a7b1c",
  "appId": "google-chrome",
  "name": "Google Chrome",
  "title": "New Tab - Google Chrome",   // title of the active window, follows its changes
  "state": "running",                   // starting | running | stopping
  "display": "wayland",                 // wayland | x11
  "audio": true,                        // has a sound sink of its own
  "instance": 2,                        // which browser of this kind it is; absent = the first
  "startedAt": "2026-11-03T00:24:07Z"
}
```

`path` is ignored when the app does not accept folders. When it does, it must be an existing absolute folder (`~` is allowed). Size is limited to 320×200 through 3840×2160, default 1280×800.

Error codes (`{ "error", "code" }`):

| Status | `code` | Meaning |
|---|---|---|
| 409 | `already_running` | The app is already open; the body also holds `"session": Session`. The client uses that session |
| 409 | `too_many_sessions` | At most 8 apps at once |
| 409 | `still_stopping` | The previous session has not finished stopping after a 15-second wait |
| 404 | `app_not_found`, `session_not_found` | |
| 400 | `bad_url` | Not an `http` or `https` address |
| 404 | `browser_not_found` | No supported browser on This PC |
| 400 | `folder_not_found` | |
| 503 | `gui_unavailable` | Packages not installed, or not Linux |
| 503 | `video_unavailable`, `audio_unavailable` | No hardware encoder or no sound; the viewer keeps using RFB |
| 409 | `too_many_encoders` | At most 16 video and audio streams at once |
| 500 | `gui_start_failed` | The display could not be created |

Events on `/api/events`:

```jsonc
{ "type": "guiSession", "session": Session }
{ "type": "guiSessionRemoved", "id": "g_…", "appId": "…", "reason": "closed" | "stopped" | "failed", "message": "…" }
```

`message` is present only when `reason` = `failed` (the app exited without a window, or the display died).

**One session per app.** Chrome and Electron apps lock their profile: opening the same profile on a second display only brings up a new window on the first display, and the second tile stays empty. That is why the hub refuses a second session and points to the existing one. The exception is a browser that a tile asks for itself (§8): its profile is separate, so they do not lock each other.

## 4. Tile and viewer

Node kind `app`, `data` = `{ "appId", "name", "sessionId" }` (see [API-board.md](API-board.md)). The hub does not validate this node, so `sessionId` can point to a session that has already ended; the tile then offers to open the app again. Deleting the tile stops its app, and undo brings the tile back closed.

`GET /api/gui/sessions/{id}/ws` passes the RFB 3.8 stream through as is, in binary frames. Any client that speaks RFB can use it, even without §7; the web uses noVNC. Several clients may watch one session, but the display size follows the client that last asked for a resize.

The web loads noVNC in a separate page `/gui-viewer.html?session=<id>&display=<wayland|x11>` inside a same-origin iframe, because pointer coordinates inside an iframe are not affected by the board's zoom. This page is the only one that may be framed (`X-Frame-Options: SAMEORIGIN`, `frame-ancestors 'self'`). Viewer and tile talk through `postMessage` with `nb: "gui"`:

- Viewer → tile: `{ "type": "ready" }` when its listener is ready. noVNC uses top-level await, so the iframe's `load` event can come first and cannot be used as the signal.
- Viewer → tile: `{ "type": "state", "state": "connected" | "disconnected", "video": bool }`. `video` false means the browser has no WebCodecs.
- Tile → viewer: `{ "type": "audio", "on": bool }`.
- Tile → viewer: `{ "type": "mic", "on": bool }`; viewer → tile: `{ "type": "mic", "state": "on" | "off" | "insecure" | "denied" | "missing" | "failed" }`.
- Tile → viewer: `{ "type": "keys", "keys": [{ "keysym", "code" }] }`: modifiers down, key pressed, modifiers up. Used by the keyboard menu in the tile header: Ctrl+T, Ctrl+W, Ctrl+Shift+T, Ctrl+Tab, Ctrl+Shift+Tab, Ctrl+N, Ctrl+Shift+N, Ctrl+Shift+W.

Clipboard:

- **Browser → app:** Ctrl+V in the tile sends the browser's clipboard text, then presses Ctrl+V in the app. No permission needed.
- **Login from Credentials:** the message `{nb:'gui',type:'enter',id,parts}` to the viewer pastes each text and presses Tab between them, then overwrites the display's clipboard; the reply is `entered {id,ok}`, and clipboard coming back from the display is ignored meanwhile ([API-vault.md](API-vault.md)).
- **App → browser:** text copied in the app goes straight to the browser's clipboard when the page uses HTTPS or localhost. Otherwise a "Copy text from the app" button appears.

## 5. Profiles

Each app always uses its own folder in `<data>/gui/profiles/<appId>/`. This folder is not deleted when the session ends.

| App | How the profile is chosen |
|---|---|
| Chromium family | `--user-data-dir`, `--password-store=basic` |
| VS Code family | `--user-data-dir`, `--extensions-dir`, `--shared-data-dir`, `--password-store=basic`, `COPILOT_HOME` |
| Firefox | `--new-instance --profile` |
| Others | `XDG_CONFIG_HOME`, `XDG_DATA_HOME`, `XDG_CACHE_HOME`, `XDG_STATE_HOME` |

Each session also has a private D-Bus bus (`dbus-run-session`), so GTK/GNOME apps do not send their windows to an instance already running on the physical screen. An app that does not follow XDG and is already open on the physical screen can still refuse to open; the hub reports it as `failed` after the app exits without a window.

The app's environment is cleaned of other sessions' markers (`VSCODE_*`, `ELECTRON_*`, `CLAUDE*`, `SSH_*`, `DISPLAY`, `DBUS_SESSION_BUS_ADDRESS` and others). Without this, the VS Code launcher would forward the request to VS Code Server.

## 6. Limits

- Shortcuts the browser holds back (Ctrl+T, Ctrl+W, Ctrl+N, Ctrl+Tab and the like) do not reach the app from the keyboard. The page cannot catch them over plain HTTP, so the tile has a keyboard menu that sends them (§4). `navigator.keyboard.lock()` works only on HTTPS and in fullscreen, and is not used.
- On Wayland, a second window brings up a row of tabs above the tile for switching; dialogs float. On X11 there is no switcher other than Alt+\`.
- Hardware video supports only VA-API so far (Intel and AMD), and is tested only on an Intel Arc A380. NVENC not yet.
- `wayvnc` still copies the image to the CPU while the screen moves, even when the viewer asks for only one pixel (about 5–14 % of one core): it cannot be paused.
- On a machine without a real sound output, such as a This PC that has only Dummy Output, the session's sink can become the default while that session lives. Apps outside the tile then play in that tile.
- Apps that ignore `PULSE_SINK` and `PIPEWIRE_NODE` are not heard in the tile. Those that ignore `PULSE_SOURCE` do not hear the tile's microphone. Apps outside the tile can pick the session's microphone source by themselves while that tile is sending sound.
- The microphone needs HTTPS or localhost, because browsers do not give it on plain HTTP (§7).
- The `<data>` path must be short enough for a Unix socket: `<data>/gui/sessions/<id>/rfb` at most 100 bytes. The default `~/.nextboard` is safe.

## 7. Video and sound

**Why two image paths.** App rendering already uses the GPU, and RFB's local latency is only 32 ms. The heavy part is sending: scrolling one page of text needs more than 100 Mbps over RFB, far above the capacity of a long-distance link. The GPU encoder needs about 6 Mbps with H.264 or 3.5 Mbps with AV1 for the same picture.

Measured text scrolling at 1280×800 on an Arc A380, on an X11 display (emulated link, clicks measured while the screen moves; the CPU cost of the Wayland display is in §8):

| Link | RFB only | RFB + H.264 | RFB + AV1 |
|---|---|---|---|
| 20 Mbps, RTT 30 ms | 5.6 fps, click response 392 ms | 47 fps, 147 ms, 6.2 Mbps | 48 fps, 132 ms, 3.4 Mbps |
| 5 Mbps, RTT 60 ms | 1.5 fps, click response 1165 ms | 43 fps, 180 ms, 3.6 Mbps | 48 fps, 165 ms, 2.2 Mbps |

**Format.** On the same text scrolling, AV1 at 3.6 Mbps gets an SSIM of 0.994 while H.264 at 6.3 Mbps gets 0.991; the Arc encoder produces both at more than 150 fps. The web asks for AV1 when the browser has a hardware decoder for it (`isConfigSupported` with `prefer-hardware`), otherwise H.264. When the hub sends `slow`, the web switches to AV1 even without a hardware decoder, before lowering the bitrate. `localStorage["nb.guiCodec"]` = `h264` or `av1` forces one format.

**Which path when.**

- RFB is always connected for input, cursor, clipboard and resize. Small changes such as typing, and everything shown while the screen is still, use RFB, which is lossless in the end, so text stays sharp.
- Large motion uses a video canvas that covers the RFB image, and RFB watches only one pixel. Large motion means 3 frames in a row exceed 25% of the per-frame bitrate budget.
- After 350 ms of calm, RFB repaints the whole image and the video canvas is hidden.
- After 2 seconds still, the encoder is turned off, so a still screen uses no CPU. The encoder starts again only when RFB sends more than 100 KB within 500 ms; its new stream starts with a key frame on the same socket.
- A tile off screen or a hidden tab stops the video and limits RFB to one pixel.

WebCodecs exists only on HTTPS and localhost. On plain HTTP the viewer keeps to RFB alone, and the tile header says so.

**Video protocol.** The `w` and `h` query values are the display size (rounded to even), `mbps` is optional (default from the frame area and format, 1–30), `codec` is optional (`av1` or `h264`, default `h264`).

- First text frame: `{"t":"hello","mbps":6.1,"codec":"h264"}`. `codec` is the format actually sent: when the GPU has no AV1, the hub sends H.264.
- Binary frame: 1 flag byte (bit 0: key frame) + one picture, H.264 Annex B or AV1 low overhead (one temporal unit).
- The client answers each decoded picture with one binary message of any content. Without answers, the hub holds the encoder back: the oldest unanswered picture may not be older than the smallest delay ever observed + 150 ms. A full link lowers the frame rate instead of adding delay.
- `{"t":"slow"}` means more than half the time went to waiting. The client should reconnect with a lower `mbps`; the web uses 60% of the current value, at least 1.5 Mbps.
- The socket is closed when the encoder stops, for example because the display got smaller. The client reconnects with the new size.

Encoder: `av1_vaapi` or `h264_vaapi`, without B-frames. On an X11 display, colors are tagged BT.709 and the conversion is on the CPU on purpose, because uploading RGB and then `scale_vaapi` on Arc skews the image at widths such as 992 and shifts colors. On Wayland, GPU buffers are converted directly (§8).

**Microphone.** `GET /api/gui/sessions/{id}/mic`, WebSocket: binary PCM frames, 48 kHz mono s16le, from the viewer to the app, 20 ms per frame. A session that has sound also has a source of its own (`Session.mic`), through a second `pw-loopback`, and its app starts with `PULSE_SOURCE`. The microphone button in the tile header is off until clicked, is not remembered, and the header says "microphone on" while it is on. Only one viewer at a time (`409 mic_busy`); `503 mic_unavailable` when the session has none, for example an old session picked up again after a hub restart. On plain HTTP `getUserMedia` does not exist, so the button says so instead of staying silent.

**Sound protocol.** Binary PCM frames, 48 kHz stereo s16le, 20 ms per frame. Silence is not sent, so a quiet app uses no bandwidth; with sound, about 1.5 Mbps. On a slow link the hub drops frames, because late sound is worse than a short gap. The tile holds sound back until there is a click on the board, per the browser's autoplay policy; the sound button in the tile header closes its socket.

Each session has a PipeWire sink of its own (`pw-loopback`, `priority.session=0`, so it never becomes the machine's default output), and the app is pointed at it through `PULSE_SINK` and `PIPEWIRE_NODE`. The sink goes away with the session.

**GPU for apps.** Xtigervnc runs with `-rendernode`, so GLX apps get DRI3. Chrome and Electron already use the GPU through ANGLE with or without this flag, including hardware video decoding: a 1080p60 video takes only about 31 % of one core in the Chrome process, so there are no extra Chrome flags.

**Shutdown.** When a session is stopped, the top program gets a `SIGTERM` first and is waited on for up to 4 seconds before the session bus, the helpers and finally the display are shut down. Session processes are recognized by their environment marker and by their process ancestry, because Chrome overwrites the memory that `/proc/<pid>/environ` reads. Chrome is recorded as having exited normally, so there is no "Restore pages?" offer when it opens again.

## 8. External sites in a web tile

A web tile (node `web`) shows two things: dev servers and light external sites through the preview proxy ([API-studio.md §4](API-studio.md)), and the rest through a **real browser on This PC**, on the same path as the app tile.

**Why a browser is still needed.** The proxy is enough for a single-host site, but still breaks on logins through another host, third-party cookies and origin checks, and it makes all sites share one hostname with the hub. A real browser has none of those problems: tabs, address bar, logins, downloads, DevTools, sound and video are all its own.

**Flow.** An address that resolves to a Tailscale or public IP is tried through the hub's proxy, without a browser ([API-studio §4.9](API-studio.md)), unless the tile or a choice remembered for that host asks for a browser. Other addresses, and sites that fail the first test, call `POST /api/gui/browser/open`:

- Browser not open yet: the hub starts it with that address, sized from the tile (`201`).
- Browser already open: the address becomes a new tab (`200`, `newTab: true`). The hub runs the browser program once more on the same profile; that program hands the address to the running instance through the profile lock and exits. This also works for a session picked up again after a hub restart.

`session` is an ordinary session (§3), so it shares the session and profile with a Desktop app tile for the same app.

**More than one tile.** When an address is typed into a new tile while the board already shows a browser, that tile asks: **New tab there** (the empty tile is deleted, the old tile is selected and framed) or **Browser here**. `here: true` starts a browser of its own for that tile, at most 4 per kind (`409 too_many_sessions`). Each browser uses a profile of its own (`<data>/gui/profiles/<appId>~2/`), so **logins are not shared between tiles** and each one needs its own login. This also means two accounts can sit side by side. The tile remembers `instance` in the node, so when opened again it gets its own profile; when that number is in use, it gets the lowest free number. One Chrome with two windows on two outputs does share logins and saves about 200 MB (see Memory), but it needs RFB and video per output, and has no counterpart on an X11 display.

**Browser choice.** A browser session that is already running is used first. Otherwise NextBoard Chromium when it has been downloaded (§8.3), then desktop entries with the `WebBrowser` category, in the order Chrome, Chromium, Brave, Edge, Vivaldi, Opera, Firefox, with snap or flatpak packages after all regular installs. Web apps installed from a browser are left out, because they do not have the `WebBrowser` category.

**Firefox.** It hands the address to the running instance through the session bus, so each Firefox session has a `dbus-daemon` of its own in `<session>/bus`, which is also found again after a hub restart. It starts with `--new-instance --profile`, and a new tab with `--profile --new-tab`. A `user.js` in a new profile turns off the default browser question and the welcome page.

**Snap.** Snap's AppArmor allows `connect` only to `/run/user/<uid>/wayland-<number>…`, and what it judges is the path where the socket was bound, so a link to a private socket is refused (tried on a real snap). That is why the compositor of such a session puts its socket in the real runtime folder, under a name libwayland picks and reports through sway's `exec`; `WAYLAND_DISPLAY` holds only the name, because `snap run` makes its link in the snap's runtime folder. The profile is in `~/snap/<name>/common/nextboard` (`nextboard-2` for a second tile; `nextboard-<instance>` and `nextboard-<instance>~2` for a named instance, so two instances' tiles never share logins or fight over a profile's lock; `~` because an instance's name may end in `-2`) and survives snap updates. The bus is the user's session bus, the only one it may reach. On an X11 display, the cookie is copied to the profile folder.

**Flatpak.** Flags go after the app id, the `--file-forwarding` and `@@` markers are dropped, and the profile is in `~/.var/app/<id>/nextboard`, named per instance and tile as for snap.

**Verification status.** The regular Chromium family and Firefox are tested with real browsers. Firefox 159 from the official tarball, through `NEXTBOARD_REAL_FIREFOX=<program> go test ./internal/gui -run TestRealFirefox` (not in CI), on Wayland and X11 displays: first page, new tab through the bus, new tab after a hub restart, second tile, and no processes left over. Each step is proven by a request that actually reached the test server. On Ubuntu that tarball gets no user namespace (`unshare(CLONE_NEWPID): EPERM`), so its process sandbox is weaker than Firefox from a package; this is a property of the tarball and of Ubuntu's AppArmor, not of the hub. Snap is tested up to the Wayland handshake between a real sway and a process inside a real snap AppArmor confinement, without a snap browser. Flatpak only its command line. On the last two paths, handing a tab to a running browser has never been tried.

**Firefox is not the first choice.** When This PC has a Chromium-family browser, that is the one started. There is no setting yet to choose the tile's browser.

**Address security.** The address goes onto the browser's command line, so only `http` and `https` with a valid host get through; flags, `file:`, `javascript:`, `chrome:` and `about:` are refused (`400 bad_url`). The URL is rebuilt and placed after the `Exec` field codes are resolved, so a `%` inside it is not read as a field code. The browser can reach everything This PC can reach, including `localhost` services and the local network, the same as the terminal access that already exists (§1).

**Node.** The web tile's `data` gets `sessionId`, `appId`, `name` next to `url`. `url` is the address that was typed, not the current page: the current page is in the browser's address bar and the session title.

- A new tile starts a browser only after an address is typed. A reopened board, old nodes and nodes from the planner show an **Open here** button, and never start a browser by themselves.
- The power button, or "Close and clear page" in the menu, closes the browser. Deleting the tile also closes it, and undo brings the tile back closed. A copy of the tile does not carry `sessionId`.
- Failed to open (packages missing, not Linux): the hub's message shows in the tile with the choices **Browser tab** and **Link card**.
- Clients that do not know this path yet still show the node from its `url` as an external site.

**Limits.** §6 applies, especially Ctrl+T, Ctrl+W and Ctrl+N, which the user's browser holds back: use the `+` and `×` buttons in Chrome. Sites see This PC's IP address. Logins are stored in `<data>/gui/profiles/<appId>/` (§1). Two sites with one login can still sit side by side in one tile through Chrome's built-in Split View (right-click a tab, "New split view with current tab"; tested on Chrome 156).

**Memory.** Measured on This PC, median of 3 runs, GitHub and Wikipedia, including `sway` and `wayvnc`, not including the encoder: Split View in one tile 740 MB; one Chrome with two windows on two headless outputs 771 MB (spike, not in the product yet); two Chromes with two profiles 971 MB. The difference between runs is about 100 MB.

**Verification (This PC, 2026-11-03):** `go test ./internal/gui -run 'TestWebURL|TestBrowserChoice|TestOpenURL'`, `internal/server TestGUIBrowserOpen`. A real Chrome 156 on a Wayland display, driven by Playwright through the hub and a temporary web build: github.com, Wikipedia, example.com, iana.org; typing in Chrome's address bar from the tile; a second tab; hub restart, then a new tab; close, reload the board without a new Chrome, open again; delete the tile.

### 8.3 NextBoard Chromium (2026-11-11)

A browser for web tiles that the hub downloads and maintains itself: **Chrome for Testing, stable channel** from Google (index `last-known-good-versions-with-downloads.json`), `linux64` and `linux-arm64` builds. It is Chromium with Chrome's codecs (H.264, AAC, VP9 tested) that still accepts `--load-extension`, while branded Chrome has ignored it since 137. So the passkey extension (§8, API-vault §passkey) loads by itself in each tile. It is no lighter than Chrome: 409 MB on disk (Chrome 156: 446 MB), same engine.

**Download** only on request, from Settings › Previews & studios:
- The index is read from a fixed address; the zip only from `https://storage.googleapis.com` without a port, redirects to another host are refused, at most 600 MB.
- The zip may hold only regular files and folders under its platform folder. A path that escapes, a link or a device rejects the whole release.
- `chrome --version` must name that version before the folder is moved to `<data>/browsers/chrome-<version>/`. `state.json` stores `installed`, `latest`, `checkedAt`, `autoUpdate`.

**Update.** Checked 2 minutes after the hub starts, then hourly once 24 hours have passed, while installed. A newer stable release is downloaded when `autoUpdate` is on (on by default), the way Chrome updates itself. An open tile keeps its version until it is closed; old versions are deleted once unused, and sessions adopted after a restart (their version is unknown) postpone the deletion.

**Browser choice.** A synthetic entry `nextboard-chromium` (category `WebBrowser`, icon `product_logo_48.png`) comes first, before installed browsers.

**The "for automated testing" bar.** Chrome for Testing shows a permanent bar "only for automated testing … use a standard version of Chrome that updates automatically". `--disable-infobars` hides it (tried; `--test-type` does not). `--enable-automation` is not used, because it makes `navigator.webdriver` true.

**Sandbox.** Ubuntu 23.10+ (`kernel.apparmor_restrict_unprivileged_userns = 1`) allows user namespaces only for programs named by an AppArmor profile, and Chrome for Testing without one stops with "No usable sandbox". The order:
1. The restriction is off, or `/etc/apparmor.d/nextboard-browser` exists (`nextboard-browser-<instance>` for a named instance, with a profile of that name): Chromium's own namespace sandbox.
2. A setuid root helper from a packaged browser exists (`/opt/google/chrome/chrome-sandbox`, Chromium, Brave, Edge, Vivaldi): `CHROME_DEVEL_SANDBOX=<helper>`. Tested with Chrome 156's helper for CfT 153 and 157.
3. Otherwise the tile does not start (`409 browser_sandbox`) and Settings shows `sudo nextboard browser-sandbox --data-dir <data>` once (with `--instance <name>` for a named instance). That command writes a `flags=(unconfined) { userns, }` profile for `<data>/browsers/chrome-*/chrome-linux*/chrome`, the same as Ubuntu's profile for Chrome, then runs `apparmor_parser -r`. `--print` only prints it. Each instance has its own file and profile name, so running it for one instance never replaces another's.

`--no-sandbox` is never used.

| Method | Path | Request | Response |
|---|---|---|---|
| GET | `/api/gui/browser` | — | `{id, name, supported, installed?, latest?, checkedAt?, autoUpdate, state: idle\|checking\|downloading\|installing\|error, done?, total?, error?, sandbox?: namespaces\|helper\|setup, sandboxHelper?, setupCommand?, dir?}` |
| POST | `/api/gui/browser/install` | — | `202` status; downloads the latest stable release, or updates to it. `409 browser_busy`; `501 browser_unsupported` (not Linux x86-64/ARM64) |
| POST | `/api/gui/browser/check` | — | `202`; only reads the index, and updates when `autoUpdate` is on and it is installed |
| PATCH | `/api/gui/browser` | `{autoUpdate}` | status |
| DELETE | `/api/gui/browser` | — | status; `409 browser_in_use` while a tile runs it. Tile profiles stay |

Event `{"type":"guiBrowser","browser":status}` on every change, including download progress (at most twice per second).

**Less memory (every Chromium-family browser in a tile).** `--disable-features=MediaRouter,OptimizationHints,OptimizationGuideModelDownloading,OptimizationHintsFetching,OptimizationTargetPrediction` turns off casting and the models and hints Chrome downloads in the background. Measured with CfT 153 headless, six tabs from two local sites, PSS of all processes: 718 and 723 MB → 664 MB (−7 %). Tried with no gain: `--process-per-site` (726 MB), `--disable-gpu` (663 MB). Dropped because it removes features: `--disable-software-rasterizer` (636 MB, but no WebGL on a machine without a GPU). Memory Saver (discarding background tabs) not tried yet: it needs hours of measuring.

## 9. Zero-copy display

With Xtigervnc, the image travels GPU → CPU → GPU: the app renders on the GPU, the X server reads it out, ffmpeg copies and converts it, then uploads it to the encoder. The hub picks the Wayland display when `sway`, `wayvnc`, `wf-recorder`, a GPU and a hardware encoder are available, and X11 when not. Each session has a compositor of its own:

- `sway` headless (`WLR_BACKENDS=headless`) renders on the GPU. Apps hand it GPU buffers.
- `wf-recorder` passes those buffers to the encoder without leaving the GPU, and only when the screen changes.
- `wayvnc` serves RFB for input, cursor, clipboard, resize and the image while still, through the same bridge and viewer.

Measured text scrolling at 1280×800 with AV1, in percent of one core:

| | X11 | Wayland |
|---|---|---|
| App (Chrome) | 42 | 30–43 |
| Display (Xtigervnc / sway) | 18 | 6 |
| Encoder (ffmpeg / wf-recorder) | 44 | 8 |
| RFB while watching one pixel (wayvnc) | – | 6 |
| **Total** | **~104** | **50–65** |
| GPU render / video-enhance | 28 % / 16 % | 20 % / 4 % |
| Click response on a calm page | 32 ms | 32 ms |
| Click response while scrolling, localhost | 83–98 ms | 110–130 ms |

The price of this display is in the last row: during large motion, click response is about 30 ms slower, because the image waits for the compositor's frame turn. Typing and ordinary clicks are not affected. `max_render_time 4` already saves 9 ms of that gap; `wf-recorder --no-damage` was not shown to help. When this matters more than CPU, `NEXTBOARD_GUI_BACKEND=x11` goes back to the old display.

On limited links, the Wayland display stays far above RFB alone:

| Link | AV1 | H.264 |
|---|---|---|
| 20 Mbps, RTT 30 ms | 42 fps, 167 ms, 3.7 Mbps | 46 fps, 163 ms, 6.3 Mbps |
| 5 Mbps, RTT 60 ms | 42 fps, 195 ms, 2.2 Mbps | 42 fps, 215 ms, 3.8 Mbps |

**What differs from X11:**

- **Apps:** the Chromium and VS Code families run natively (`--ozone-platform=wayland`) with no X server at all. Other apps get Xwayland as a fallback.
- **Xwayland** from wlroots uses no cookie, so any local user can connect. The hub runs it from the start (`xwayland force`) and sets its access list to `localuser:<hub user>` through `ChangeHosts` before the app runs. If that fails, the session does not open. That list would be lost if Xwayland restarted, which is why lazy mode is not used.
- **Color:** the GPU conversion always produces BT.601, so the stream is tagged `smpte170m`, and the colors are right.
- **Rate control:** `wf-recorder` uses a microsecond time base, and without `-r 60` the VA-API encoder would assume 1 million fps, so text turns blocky.
- **Pipe latency:** avio holds data back up to 32 KB, so the output uses the `tee` muxer with `flush_packets=1`.
- **RFB:** the viewer prefers ZRLE, because `wayvnc` has no lossless refresh like Xtigervnc. Text while still stays exact. Keys are sent as keysyms (without `QEMUExtendedKeyEvent`) so keyboard layouts other than US come out right. Pasted text is sent as `Provide` without `Notify`, because the `Notify` flow takes one RTT and the Ctrl+V key would arrive first.
- **`wayvnc` crashes:** `wayvnc` 0.9.1 segfaults now and then. The hub restarts it within ~2 seconds, the viewer reconnects by itself, and the app is not disturbed.
- **`XDG_RUNTIME_DIR`:** the compositor uses a private folder `$XDG_RUNTIME_DIR/nb-<id>`. Apps keep using their original folder because they need PipeWire, with `WAYLAND_DISPLAY` holding an absolute path.
