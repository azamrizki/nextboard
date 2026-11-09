// The page inside a desktop app tile (docs/API-gui.md §4). It lives in its own same-origin
// iframe: pointer coordinates in there are the iframe's own whatever the board zoom (noVNC
// maps them with the canvas box, which a CSS scale on a parent would distort), and noVNC's
// keyboard capture stays away from the board's shortcuts.
//
// Two pictures of one display (§7). RFB always runs for input, cursor, clipboard and resize,
// and shows small changes and sharp text. Video from the GPU covers it during big motion,
// when RFB would need over 100 Mbps; RFB then only watches one pixel. When things calm down,
// RFB repaints the whole picture sharply and the video canvas goes away again.

import RFB from '@novnc/novnc';
import { AudioOutput } from './audio';
import { MicInput, type MicState } from './mic';
import { prefs } from '../lib/format';
import { av1Decoding, VideoStream, type VideoCodec } from './video';

const params = new URLSearchParams(location.search);
const session = params.get('session') ?? '';
const screen = document.getElementById('screen') as HTMLElement;
const overlay = document.getElementById('video') as HTMLCanvasElement;
const copyButton = document.getElementById('copy') as HTMLButtonElement;

const audio = new AudioOutput(session);
const mic = new MicInput(session, (state: MicState, message?: string) => window.parent.postMessage({ nb: 'gui', type: 'mic', state, message }, location.origin));

const XK_v = 0x76;
const XK_V = 0x56;

let rfb: RFB | null = null;
let connected = false;
let retry = 1000;

function report(state: 'connected' | 'disconnected') {
  window.parent.postMessage({ nb: 'gui', type: 'state', state, video: 'VideoDecoder' in window }, location.origin);
}

function connect() {
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws';
  const client = new RFB(screen, `${scheme}://${location.host}/api/gui/sessions/${encodeURIComponent(session)}/ws`, { shared: true });
  // The display follows the tile. Scaling only shows while another viewer holds a different size.
  client.resizeSession = true;
  client.scaleViewport = true;
  client.background = '#0d1117';
  client.qualityLevel = 7;
  client.compressionLevel = 2;
  client.addEventListener('connect', () => {
    retry = 1000;
    connected = true;
    report('connected');
  });
  client.addEventListener('disconnect', () => {
    if (rfb === client) rfb = null;
    connected = false;
    report('disconnected');
    // The tile drops this page once the session is gone, so a drop here is the network.
    window.setTimeout(connect, retry);
    retry = Math.min(retry * 2, 5000);
  });
  client.addEventListener('clipboard', (e) => copyOut((e as CustomEvent<{ text: string }>).detail.text));
  rfb = client;
}

// ---- RFB's part of the picture ----

const sendRequest = RFB.messages.fbUpdateRequest;
const sendContinuous = RFB.messages.enableContinuousUpdates;
let rfbSock: unknown = null;
let continuous = false;
/** RFB only watches one pixel: input, cursor, clipboard and resize still work. */
let narrow = false;

RFB.messages.fbUpdateRequest = (sock, incremental, x, y, w, h) => {
  rfbSock = sock;
  if (narrow) sendRequest(sock, true, 0, 0, 1, 1);
  else sendRequest(sock, incremental, x, y, w, h);
};
RFB.messages.enableContinuousUpdates = (sock, enable, x, y, w, h) => {
  rfbSock = sock;
  continuous = enable;
  if (narrow) sendContinuous(sock, enable, 0, 0, 1, 1);
  else sendContinuous(sock, enable, x, y, w, h);
};

// Keys travel as what they type, not as positions on a keyboard. wayvnc takes the positions
// at face value for a US layout, so any other layout would get the wrong characters.
const QEMU_KEY_EVENTS = -258;
const ENCODING_ZRLE = 16;
const sendEncodings = RFB.messages.clientEncodings;
RFB.messages.clientEncodings = (sock, encodings) => {
  const list = encodings.filter((e) => e !== QEMU_KEY_EVENTS);
  // wayvnc has no lossless refresh like Xtigervnc's: only ZRLE leaves text sharp at rest.
  // It costs little, as big changes go through the video.
  if (params.get('display') === 'wayland') list.unshift(ENCODING_ZRLE);
  sendEncodings(sock, list);
};

const rfbCanvas = () => screen.querySelector('canvas');

function setNarrow(on: boolean) {
  if (narrow === on) return;
  narrow = on;
  const c = rfbCanvas();
  if (!rfbSock || !c) return;
  if (on) {
    if (continuous) sendContinuous(rfbSock, true, 0, 0, 1, 1);
    return;
  }
  if (continuous) sendContinuous(rfbSock, true, 0, 0, c.width, c.height);
  sendRequest(rfbSock, false, 0, 0, c.width, c.height);
}

// ---- video ----

/** Give up on the video after this many connections in a row without a picture. */
const MAX_FAILURES = 3;
/** Calm for this long: RFB takes over and the text gets sharp again. */
const SETTLE_MS = 350;
/** Time RFB gets to repaint before the video is taken away. */
const REPAINT_MS = 250;
/** Wait for the size to stop changing, as a new size means a new encoder. */
const SIZE_STABLE_MS = 400;
const RESIZE_RETRY_MS = 1000;
const MIN_MBPS = 1.5;
/** AV1 at this share of a H.264 bitrate looks at least as good. */
const AV1_RATE_SHARE = 0.6;
const TICK_MS = 100;

let video: VideoStream | null = null;
let mbps = 0;
/** null until known: the first stream waits for the browser's answer. */
let codec: VideoCodec | null = null;
let av1Possible = false;
let failures = 0;
let gotPicture = false;
let retryAt = 0;
let lastSize = '';
let sizeAt = 0;
let shown = true;
let inView = true;
let overlayOn = false;
let lastMotion = 0;
let bigRun = 0;
let settleTimer = 0;

/**
 * Pictures take more than this share of the bitrate's budget per frame: big motion. The
 * size says little by itself, as the encoder fills whatever rate it has. A blinking cursor
 * stays below; the lower bound keeps it there on a slow link. The hub
 * (internal/server/h_gui.go) decides when the encoder can sleep with the same numbers.
 */
const motionBytes = () => Math.max(800, (Math.max(mbps, MIN_MBPS) * 1e6 * 0.25) / 8 / 60);
/** So many big pictures in a row: one repaint or typed key doesn't make motion. */
const MOTION_RUN = 3;
/** Once moving, it takes much less to count as still moving: no flipping back and forth. */
const KEEP_MOTION_SHARE = 1 / 4;

function showOverlay() {
  window.clearTimeout(settleTimer);
  settleTimer = 0;
  overlayOn = true;
  overlay.hidden = false;
  if (video) video.draw = true;
  setNarrow(true);
}

function hideOverlay() {
  window.clearTimeout(settleTimer);
  settleTimer = 0;
  overlayOn = false;
  overlay.hidden = true;
  if (video) video.draw = false;
  setNarrow(false);
}

function startVideo(c: HTMLCanvasElement, wanted: VideoCodec) {
  gotPicture = false;
  const stream: VideoStream = new VideoStream(session, overlay, c.width, c.height, mbps, wanted, {
    onHello: (m, actual) => {
      mbps = m;
      codec = actual;
    },
    onFrame: (bytes, key) => {
      gotPicture = true;
      failures = 0;
      if (key) return;
      const limit = motionBytes();
      bigRun = bytes > limit ? bigRun + 1 : 0;
      if (bigRun >= MOTION_RUN || (overlayOn && bytes > limit * KEEP_MOTION_SHARE)) {
        lastMotion = performance.now();
        if (!overlayOn || settleTimer) showOverlay();
      }
    },
    onSlow: () => {
      if (codec === 'h264' && av1Possible) {
        // More picture per bit is worth the decoding cost now.
        codec = 'av1';
        mbps = Math.max(MIN_MBPS, mbps * AV1_RATE_SHARE);
      } else {
        if (mbps <= MIN_MBPS) return;
        mbps = Math.max(MIN_MBPS, mbps * 0.6);
      }
      stopVideo();
    },
    onClosed: () => {
      if (video !== stream) return;
      video = null;
      if (overlayOn) hideOverlay();
      if (!gotPicture) failures++;
      retryAt = performance.now() + 1000 * Math.max(failures, 1);
    },
  });
  stream.draw = overlayOn;
  video = stream;
}

function stopVideo() {
  const v = video;
  video = null;
  v?.stop();
}

function updateShown() {
  const now = inView && document.visibilityState === 'visible';
  if (now === shown) return;
  shown = now;
  if (now) {
    failures = 0;
    setNarrow(false);
    return;
  }
  stopVideo();
  hideOverlay();
  // Nobody looks: RFB can rest as well, until the picture is needed again.
  setNarrow(true);
}
document.addEventListener('visibilitychange', updateShown);
new IntersectionObserver((entries) => {
  inView = entries[entries.length - 1].isIntersecting;
  updateShown();
}).observe(document.documentElement);

function tick() {
  const c = rfbCanvas();
  if (!c) return;
  const now = performance.now();
  // The overlay lies exactly over the RFB picture, whatever noVNC's scale.
  const box = c.getBoundingClientRect();
  const scale = c.width > 0 ? box.width / c.width : 1;
  overlay.style.left = `${box.left}px`;
  overlay.style.top = `${box.top}px`;
  overlay.style.width = `${(c.width & ~1) * scale}px`;
  overlay.style.height = `${(c.height & ~1) * scale}px`;

  const size = `${c.width}x${c.height}`;
  if (size !== lastSize) {
    lastSize = size;
    sizeAt = now;
    stopVideo();
    if (overlayOn) hideOverlay();
  }
  // A size request made while the display is still changing size can get lost.
  if (rfb && connected && now - sizeAt >= RESIZE_RETRY_MS && (c.width !== screen.clientWidth || c.height !== screen.clientHeight)) {
    sizeAt = now;
    rfb.resizeSession = true;
  }
  if (!video && codec && connected && shown && failures < MAX_FAILURES && now >= retryAt && now - sizeAt >= SIZE_STABLE_MS && c.width >= 64) {
    startVideo(c, codec);
  }
  if (overlayOn && !settleTimer && now - lastMotion >= SETTLE_MS) {
    setNarrow(false);
    settleTimer = window.setTimeout(hideOverlay, REPAINT_MS);
  }
  audio.gesture();
}
window.setInterval(tick, TICK_MS);

// ---- audio ----

for (const type of ['pointerdown', 'keydown']) window.addEventListener(type, () => audio.gesture(), true);

window.addEventListener('message', (e) => {
  if (e.origin !== location.origin || e.source !== window.parent) return;
  const m = e.data as { nb?: string; type?: string; on?: boolean; id?: string; parts?: EnterPart[]; keys?: SentKey[] } | null;
  if (m?.nb === 'gui' && m.type === 'audio') audio.setWanted(m.on === true);
  if (m?.nb === 'gui' && m.type === 'mic') void (m.on === true ? mic.start() : mic.stop());
  if (m?.nb === 'gui' && m.type === 'keys') pressKeys(m.keys ?? []);
  if (m?.nb === 'gui' && m.type === 'enter') void enterParts(m.id ?? '', m.parts ?? []);
});

// ---- keys the browser keeps for itself ----
// Ctrl+T and its like never reach this page, so they come from the tile's menu: the modifiers go
// down, the key is pressed, and the modifiers come up again in reverse order.

type SentKey = { keysym: number; code: string };

function pressKeys(keys: SentKey[]) {
  if (!rfb || !keys.length) return;
  for (const k of keys) rfb.sendKey(k.keysym, k.code, true);
  for (const k of [...keys].reverse()) rfb.sendKey(k.keysym, k.code, false);
  rfb.focus();
}

// ---- a login from Credentials ----
// Pasted and not typed: wayvnc silently drops whatever its keymap lacks ($, ä, я, ...), and a
// password short of one character is wrong in a way nobody can see. A paste arrives whole or not
// at all. It sits on that display's clipboard only for the moment it takes, and does not come back
// to this browser's (copyOut).

type EnterPart = { text?: string; key?: string };

const XK_Tab = 0xff09;
const XK_Control_L = 0xffe3;
/** The text reaches the display before the key that asks for it. */
const PROVIDE_MS = 60;
/** The app has read the clipboard and is ready for whatever comes next. */
const PASTED_MS = 350;
/** Long enough for the display to report its clipboard back, which is not news to anyone. */
const ECHO_MS = 1000;

let entering = 0;
const pause = (ms: number) => new Promise((resolve) => window.setTimeout(resolve, ms));

async function enterParts(id: string, parts: EnterPart[]) {
  const ok = !!rfb && connected;
  if (ok) {
    entering++;
    for (const part of parts) {
      if (part.key === 'tab') {
        rfb?.sendKey(XK_Tab, 'Tab');
        await pause(PROVIDE_MS);
      }
      if (part.text) {
        pasteIn(part.text);
        await pause(PROVIDE_MS);
        rfb?.sendKey(XK_Control_L, 'ControlLeft', true);
        rfb?.sendKey(XK_v, 'KeyV');
        rfb?.sendKey(XK_Control_L, 'ControlLeft', false);
        await pause(PASTED_MS);
      }
    }
    pasteIn(' ');
    window.setTimeout(() => entering--, ECHO_MS);
  }
  window.parent.postMessage({ nb: 'gui', type: 'entered', id, ok }, location.origin);
}

// ---- clipboard: app → browser ----

let offered = '';
let offerTimer = 0;

function copyOut(text: string) {
  if (entering) return; // our own paste of a login, on its way back
  // Needs a secure context and focus; plain http or a background tile falls back to a click.
  const write = navigator.clipboard?.writeText(text);
  if (write) write.catch(() => offerCopy(text));
  else offerCopy(text);
}

function offerCopy(text: string) {
  offered = text;
  copyButton.hidden = false;
  window.clearTimeout(offerTimer);
  offerTimer = window.setTimeout(() => {
    copyButton.hidden = true;
  }, 15000);
}

copyButton.addEventListener('click', () => {
  const area = document.createElement('textarea');
  area.value = offered;
  document.body.append(area);
  area.select();
  document.execCommand('copy');
  area.remove();
  copyButton.hidden = true;
  rfb?.focus();
});

// ---- clipboard: browser → app ----
// Ctrl+V is held back from noVNC, which would cancel the browser's paste. The paste event
// needs no permission or secure context; the text reaches the app first, then the key.

const CLIPBOARD_TEXT = 1;
let pendingKey: { keysym: number; timer: number } | null = null;

function sendPasteKey() {
  if (!pendingKey) return;
  window.clearTimeout(pendingKey.timer);
  rfb?.sendKey(pendingKey.keysym, 'KeyV');
  pendingKey = null;
}

window.addEventListener(
  'keydown',
  (e) => {
    if (!e.ctrlKey || e.altKey || e.metaKey || e.code !== 'KeyV') return;
    e.stopImmediatePropagation();
    if (pendingKey) window.clearTimeout(pendingKey.timer);
    // Nothing textual on the clipboard fires no paste event: the app still gets its key.
    pendingKey = { keysym: e.shiftKey ? XK_V : XK_v, timer: window.setTimeout(sendPasteKey, 200) };
  },
  true,
);

/**
 * wayvnc only has the text after asking for it, a round trip that the key behind it would
 * win, pasting nothing. Sent without waiting to be asked, the text arrives before the key.
 */
function pasteIn(text: string) {
  if (params.get('display') === 'wayland' && rfbSock) RFB.messages.extendedClipboardProvide(rfbSock, [CLIPBOARD_TEXT], [text]);
  else rfb?.clipboardPasteFrom(text);
}

window.addEventListener('paste', (e) => {
  const text = e.clipboardData?.getData('text/plain');
  if (text) pasteIn(text);
  e.preventDefault();
  sendPasteKey();
});

connect();
if ('VideoDecoder' in window) {
  void av1Decoding().then(({ hardware, software }) => {
    // nb.guiCodec = "h264" or "av1" in local storage overrides the choice for good.
    const forced = prefs.get('guiCodec');
    const fixed = forced === 'av1' || forced === 'h264';
    av1Possible = software && !fixed;
    codec = fixed ? forced : hardware ? 'av1' : 'h264';
  });
}
// noVNC has a top-level await, so the iframe's load event can come before this module runs
// and its listeners exist. Only now can the tile talk to this page.
window.parent.postMessage({ nb: 'gui', type: 'ready' }, location.origin);
