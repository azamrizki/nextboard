// Binds an xterm.js instance in `containerRef` to /api/sessions/{id}/ws.
//
// Protocol (docs/API.md):
//   server → client: binary = raw output (first binary frame after each connect is the
//                    scrollback snapshot → term.reset() first); text = {"t":"status",…}
//   client → server: {"t":"i","d":"…"} input, {"t":"r","c":cols,"r":rows} resize

import { useEffect, useRef, useState, type RefObject } from 'react';
import { Terminal, type ITheme } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import { WebglAddon } from '@xterm/addon-webgl';
import '@xterm/xterm/css/xterm.css';
import { api, openSessionSocket, type SocketState } from '../api';
import { useTheme } from '../lib/theme';
import type { SessionStatusFrame } from '../lib/types';
import { store } from '../store';
import { applyMods, registerConsole } from './consoleKeys';
import { clipboardFiles, sendPasted } from './files';

/** The console's colours (tokens.css, DESIGN.md §3.9), for the theme around this element. */
function themeOf(el: Element): { theme: ITheme; minimumContrastRatio: number } {
  const css = getComputedStyle(el);
  const t = (name: string) => css.getPropertyValue(`--term-${name}`).trim();
  return {
    theme: {
      background: t('bg'),
      foreground: t('fg'),
      cursor: t('cursor'),
      cursorAccent: t('cursor-accent'),
      selectionBackground: t('selection'),
      black: t('black'),
      red: t('red'),
      green: t('green'),
      yellow: t('yellow'),
      blue: t('blue'),
      magenta: t('magenta'),
      cyan: t('cyan'),
      white: t('white'),
      brightBlack: t('bright-black'),
      brightRed: t('bright-red'),
      brightGreen: t('bright-green'),
      brightYellow: t('bright-yellow'),
      brightBlue: t('bright-blue'),
      brightMagenta: t('bright-magenta'),
      brightCyan: t('bright-cyan'),
      brightWhite: t('bright-white'),
    },
    minimumContrastRatio: Number(t('min-contrast')) || 1,
  };
}

// Bundled locally via @fontsource-variable/jetbrains-mono (imported in main.tsx).
const FONT_FAMILY = '"JetBrains Mono Variable", "JetBrains Mono", ui-monospace, Menlo, Consolas, monospace';
const FONT_SIZE = 13;

/**
 * Resolves once JetBrains Mono is loaded (or after a short timeout), so xterm measures
 * the real cell size on open instead of a fallback font's. Shared by all terminals.
 */
let fontReady: Promise<void> | null = null;
function waitForMonoFont(): Promise<void> {
  if (!fontReady) {
    const load = document.fonts
      ? Promise.all([
          document.fonts.load(`${FONT_SIZE}px "JetBrains Mono Variable"`),
          document.fonts.load(`bold ${FONT_SIZE}px "JetBrains Mono Variable"`),
        ]).then(() => undefined)
      : Promise.resolve();
    const timeout = new Promise<void>((resolve) => window.setTimeout(resolve, 2500));
    fontReady = Promise.race([load.catch(() => undefined), timeout]);
  }
  return fontReady;
}

/**
 * WebGL only pays off on a hardware GPU. Software rasterisers (SwiftShader, llvmpipe —
 * headless browsers, VMs, GPU-blocklisted machines) are slower than xterm's DOM renderer
 * and were observed to draw nothing at devicePixelRatio 2, so we skip WebGL there.
 */
let hardwareWebgl: boolean | null = null;
function hasHardwareWebgl(): boolean {
  if (hardwareWebgl !== null) return hardwareWebgl;
  hardwareWebgl = false;
  try {
    const canvas = document.createElement('canvas');
    const gl = (canvas.getContext('webgl2') ?? canvas.getContext('webgl')) as WebGLRenderingContext | null;
    if (gl) {
      const info = gl.getExtension('WEBGL_debug_renderer_info');
      const renderer = String(info ? gl.getParameter(info.UNMASKED_RENDERER_WEBGL) : gl.getParameter(gl.RENDERER));
      hardwareWebgl = !/swiftshader|llvmpipe|softpipe|software|basic render/i.test(renderer);
      gl.getExtension('WEBGL_lose_context')?.loseContext();
    }
  } catch {
    hardwareWebgl = false;
  }
  return hardwareWebgl;
}

/** Try the WebGL renderer; on failure or context loss xterm falls back to its DOM renderer. */
function tryWebgl(term: Terminal) {
  if (!hasHardwareWebgl()) return;
  try {
    const addon = new WebglAddon();
    addon.onContextLoss(() => {
      // Browsers cap live WebGL contexts (~16); the oldest ones get dropped.
      try {
        addon.dispose();
      } catch {
        /* ignore */
      }
    });
    term.loadAddon(addon);
  } catch {
    /* WebGL unavailable → default renderer */
  }
}

interface PointerAt {
  clientX: number;
  clientY: number;
}

/** How much the board's zoom scales this element. Layout width has no rounding, unlike offsetWidth. */
function cssScale(el: HTMLElement): number {
  const layout = parseFloat(getComputedStyle(el).width);
  const shown = el.getBoundingClientRect().width;
  return layout > 0 && shown > 0 ? shown / layout : 1;
}

/** The pointer as if the element were not scaled: its corner stays, the distance from it shrinks or grows. */
function unscaled(ev: PointerAt, el: HTMLElement): PointerAt {
  const box = el.getBoundingClientRect();
  const k = cssScale(el);
  return { clientX: box.left + (ev.clientX - box.left) / k, clientY: box.top + (ev.clientY - box.top) / k };
}

/**
 * xterm takes the pointer in screen pixels and its cells in layout pixels, so under the board's zoom
 * a selection lands beside the pointer, further off the further from the corner. It has no option for
 * this, so its private methods are wrapped. Selection, mouse reports to programs and links all go
 * through the mouse service. If an upgrade renames these, zoom goes back to being wrong: DESIGN.md §3.9.
 */
function followBoardZoom(term: Terminal) {
  /* eslint-disable @typescript-eslint/no-explicit-any */
  const core = (term as any)._core;
  const mouse = core?._mouseService;
  const selection = core?._selectionService;
  const bar = core?._viewport?._scrollableElement?._verticalScrollbar;
  /* eslint-enable @typescript-eslint/no-explicit-any */
  if (!mouse?.getCoords || !mouse.getMouseReportCoords || !selection?._getMouseEventScrollAmount || !bar?._sliderPointerPosition) {
    console.warn('xterm internals changed: pointer positions are off when the board is zoomed');
    return;
  }
  const coords = mouse.getCoords.bind(mouse);
  mouse.getCoords = (ev: PointerAt, el: HTMLElement, ...rest: unknown[]) => coords(unscaled(ev, el), el, ...rest);
  const report = mouse.getMouseReportCoords.bind(mouse);
  mouse.getMouseReportCoords = (ev: PointerAt, el: HTMLElement) => report(unscaled(ev, el), el);
  // how far a drag is past the edge, which sets the speed of scrolling
  const pastEdge = selection._getMouseEventScrollAmount.bind(selection);
  selection._getMouseEventScrollAmount = (ev: PointerAt) => pastEdge(unscaled(ev, selection._screenElement));
  // the scrollbar's thumb: only its distance counts, so the scale is enough
  const thumb = bar._sliderPointerPosition.bind(bar);
  bar._sliderPointerPosition = (ev: unknown) => thumb(ev) / cssScale(selection._screenElement);
}

const IS_MAC = /Mac|iPhone|iPad/.test(navigator.platform);
const PASTE_GAP_MS = 250;

/**
 * What a console gets for a pasted file. CLIs recognise a pasted path, and make an attachment of
 * an image; Gemini wants an @ in front. Anything else needing quotes gets them, as a shell does.
 */
function pastedPath(agent: string, path: string): string {
  if (agent === 'gemini') return `@${path.replace(/[\\ ]/g, '\\$&')}`;
  return /^[\w@%+=:,./~-]+$/.test(path) ? path : `'${path.replace(/'/g, `'\\''`)}'`;
}

export function useTerminal(sessionId: string, containerRef: RefObject<HTMLDivElement | null>, fontSize = FONT_SIZE): SocketState {
  const [connState, setConnState] = useState<SocketState>('connecting');
  const live = useRef<{ term: Terminal; refit: () => void } | null>(null);
  const fontRef = useRef(fontSize);
  fontRef.current = fontSize;

  // the theme changes without re-creating the terminal; this runs after the DOM has its data-theme
  const { theme, consolePref } = useTheme();
  useEffect(() => {
    const l = live.current;
    const el = containerRef.current;
    if (l && el) Object.assign(l.term.options, themeOf(el));
  }, [theme, consolePref, containerRef]);

  // text size changes without re-creating the terminal
  useEffect(() => {
    const l = live.current;
    if (!l || l.term.options.fontSize === fontSize) return;
    l.term.options.fontSize = fontSize;
    l.refit();
  }, [fontSize]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    let disposed = false;
    let cleanup: (() => void) | null = null;

    void waitForMonoFont().then(() => {
      if (disposed) return;
      cleanup = attach(container);
    });

    return () => {
      disposed = true;
      cleanup?.();
    };

    /** Create the terminal + socket; returns the teardown function. */
    function attach(el: HTMLDivElement): () => void {
      const term = new Terminal({
        fontFamily: FONT_FAMILY,
        fontSize: fontRef.current,
        lineHeight: 1.15,
        cursorBlink: true,
        scrollback: 10000,
        ...themeOf(el),
        allowProposedApi: false,
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(el);
      followBoardZoom(term);
      tryWebgl(term);

      let awaitingSnapshot = true;
      let sent = { c: 0, r: 0 };
      // Snapshots still being parsed. A snapshot is history: the terminal queries in it (tmux
      // asks on every attach, agent CLIs on every start) were answered long ago. xterm would
      // answer them again, and the answers would land in the shell as typed text ("1;2c").
      let replaying = 0;

      const sock = openSessionSocket(sessionId, {
        onStateChange: setConnState,
        onOpen: () => {
          // Every (re)connect starts with a snapshot frame; also tell the hub our size.
          awaitingSnapshot = true;
          fitAndSend(true);
        },
        onMessage: (ev) => {
          if (typeof ev.data === 'string') {
            try {
              const msg = JSON.parse(ev.data) as { t?: string };
              if (msg.t === 'status') store.patchSessionStatus(sessionId, msg as SessionStatusFrame);
            } catch {
              /* ignore malformed frames */
            }
            return;
          }
          if (awaitingSnapshot) {
            term.reset();
            awaitingSnapshot = false;
            replaying++;
            term.write(new Uint8Array(ev.data as ArrayBuffer), () => {
              replaying--;
            });
            return;
          }
          term.write(new Uint8Array(ev.data as ArrayBuffer));
        },
      });

      // Fit to the container and report the size when it changed (or when forced on connect).
      function fitAndSend(force = false) {
        if (!el.isConnected || el.clientWidth === 0 || el.clientHeight === 0) return;
        try {
          fit.fit();
        } catch {
          return;
        }
        const c = term.cols;
        const r = term.rows;
        if (!force && c === sent.c && r === sent.r) return;
        if (sock.sendJSON({ t: 'r', c, r })) sent = { c, r };
      }

      const sendInput = (d: string) => {
        if (replaying === 0) sock.sendJSON({ t: 'i', d });
      };
      // the key bar's armed Ctrl or Alt changes the next key typed (consoleKeys.ts)
      const dataSub = term.onData((d) => sendInput(applyMods(d)));
      // Binary strings (legacy mouse reports) are passed through as-is.
      const binSub = term.onBinary(sendInput);

      // Plain Ctrl+V is the browser's paste here, because only the paste shows what is on the
      // clipboard. Until it arrives, this says the key asked for it.
      let ctrlV = false;

      // Ctrl+Shift+C copies the selection (plain Ctrl+C must reach the program as ^C).
      term.attachCustomKeyEventHandler((e) => {
        if (e.type === 'keydown' && e.ctrlKey && e.shiftKey && e.code === 'KeyC' && term.hasSelection()) {
          void navigator.clipboard?.writeText(term.getSelection()).catch(() => undefined);
          return false;
        }
        if (IS_MAC || e.code !== 'KeyV' || !e.ctrlKey || e.shiftKey || e.altKey || e.metaKey) return true;
        if (e.type === 'keydown') {
          ctrlV = true;
          return false;
        }
        if (e.type === 'keyup' && ctrlV) {
          // no paste came of it
          ctrlV = false;
          sendInput('\x16');
        }
        return true;
      });

      const attachFiles = async (files: File[]) => {
        const sent = await sendPasted(files, (f) => api.attachToSession(sessionId, f));
        const s = store.getState().sessions[sessionId];
        for (const st of sent) {
          if (disposed) return;
          term.paste(pastedPath(s?.agent || s?.preset || '', st.path));
          // CLIs open a pasted image before they show it: what follows has to wait its turn
          await new Promise((done) => window.setTimeout(done, PASTE_GAP_MS));
          if (!disposed) sendInput(' ');
        }
      };

      // A file goes to the machine this console runs on, and the console gets its path. Text is
      // xterm's. Ctrl+V with text stays ^V in a shell, where vim and readline mean something by
      // it; an agent CLI has no use for it (it would look on This PC's clipboard, not the user's).
      const onPaste = (e: ClipboardEvent) => {
        const key = ctrlV;
        ctrlV = false;
        const files = clipboardFiles(e.clipboardData);
        if (files.length) {
          e.preventDefault();
          e.stopPropagation();
          void attachFiles(files);
          return;
        }
        if (!key) return;
        const s = store.getState().sessions[sessionId];
        if (s && s.preset !== 'shell' && e.clipboardData?.getData('text/plain')) return;
        e.preventDefault();
        e.stopPropagation();
        sendInput('\x16');
      };
      el.addEventListener('paste', onPaste, true);

      const unregister = registerConsole(sessionId, {
        send: sendInput,
        appCursor: () => term.modes.applicationCursorKeysMode,
        text: (maxLines) => {
          const buf = term.buffer.active;
          const lines: string[] = [];
          for (let i = Math.max(0, buf.length - maxLines); i < buf.length; i++) lines.push(buf.getLine(i)?.translateToString(true) ?? '');
          while (lines.length && !lines[lines.length - 1]) lines.pop();
          return lines.join('\n');
        },
        attach: attachFiles,
        focus: () => term.focus(),
      });

      let resizeTimer: number | undefined;
      const ro = new ResizeObserver(() => {
        window.clearTimeout(resizeTimer);
        resizeTimer = window.setTimeout(() => fitAndSend(), 60);
      });
      ro.observe(el);
      fitAndSend();
      live.current = { term, refit: () => fitAndSend() };

      return () => {
        live.current = null;
        unregister();
        window.clearTimeout(resizeTimer);
        el.removeEventListener('paste', onPaste, true);
        ro.disconnect();
        dataSub.dispose();
        binSub.dispose();
        sock.close();
        term.dispose();
      };
    }
  }, [sessionId, containerRef]);

  return connState;
}
