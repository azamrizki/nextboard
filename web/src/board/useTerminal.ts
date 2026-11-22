// Binds an xterm.js instance in `containerRef` to /api/sessions/{id}/ws.
//
// Protocol (docs/API.md):
//   server → client: binary = raw output (first binary frame after each connect is the
//                    scrollback snapshot → term.reset() first); text = {"t":"status",…} and
//                    {"t":"size","c":cols,"r":rows}, the terminal's size, which this view shows
//   client → server: {"t":"i","d":"…"} input, {"t":"r","c":cols,"r":rows} the size this view
//                    would take, {"t":"f"} focused. The view last typed into or focused decides
//                    the size; the others follow it (docs/API.md), so two views of one console
//                    never draw for one size what they show at another.

import { useEffect, useRef, useState, type RefObject } from 'react';
import { Terminal, type ITheme } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { api, openSessionSocket, type SocketState } from '../api';
import { useTheme } from '../lib/theme';
import type { SessionStatusFrame } from '../lib/types';
import { store } from '../store';
import { applyMods, registerConsole } from './consoleKeys';
import { clipboardFiles, sendPasted } from './files';
import { watchFileDrop } from '../lib/fileDrop';
import { registerTerminal } from './webglPool';
import { SnapshotFeed } from './snapshotQueue';
import { disposeTerminal } from './xtermPatches';

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
// Lines kept above the screen. Each line costs memory per terminal (10000 lines of a 180-col
// tile ≈ 25 MB), and the hub replays only its 1 MiB ring on reconnect, so lines past that
// never come back after a reload anyway.
const SCROLLBACK_LINES = 5000;

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

/** Any of it inside the window. */
function onScreen(el: HTMLElement): boolean {
  if (!el.isConnected) return false;
  const r = el.getBoundingClientRect();
  return r.width > 0 && r.height > 0 && r.right > 0 && r.bottom > 0 && r.left < window.innerWidth && r.top < window.innerHeight;
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
        scrollback: SCROLLBACK_LINES,
        ...themeOf(el),
        allowProposedApi: false,
      });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(el);
      followBoardZoom(term);

      let awaitingSnapshot = true;
      let sent = { c: 0, r: 0 };
      // the terminal's size as the hub says (size frames); until one comes (an older hub), the
      // view fits itself as before
      let hubSize: { c: number; r: number } | null = null;
      const takeHubSize = () => {
        if (hubSize && (term.cols !== hubSize.c || term.rows !== hubSize.r)) term.resize(hubSize.c, hubSize.r);
      };
      // Snapshots take turns across consoles (snapshotQueue.ts): the focused one, then those on
      // screen. While one is parsed, input is held back: it is history, the terminal queries in it
      // (tmux asks on every attach, agent CLIs on every start) were answered long ago, and xterm's
      // answers would land in the shell as typed text ("1;2c").
      const feed = new SnapshotFeed(term, () => (el.contains(document.activeElement) ? 2 : onScreen(el) ? 1 : 0));

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
              const msg = JSON.parse(ev.data) as { t?: string; c?: number; r?: number };
              if (msg.t === 'status') store.patchSessionStatus(sessionId, msg as SessionStatusFrame);
              else if (msg.t === 'size' && msg.c && msg.r) {
                hubSize = { c: msg.c, r: msg.r };
                takeHubSize();
              }
            } catch {
              /* ignore malformed frames */
            }
            return;
          }
          const data = new Uint8Array(ev.data as ArrayBuffer);
          if (awaitingSnapshot) {
            awaitingSnapshot = false;
            feed.snapshot(data);
            return;
          }
          feed.live(data);
        },
      });

      // Tell the hub the size this view would take (when it changed, or forced on connect). The
      // screen itself shows the terminal's size, from the hub: this view's own size once it is
      // the one last used, another view's while that one is.
      function fitAndSend(force = false) {
        if (!el.isConnected || el.clientWidth === 0 || el.clientHeight === 0) return;
        let want: { cols: number; rows: number } | undefined;
        try {
          want = fit.proposeDimensions();
          if (!hubSize) fit.fit();
        } catch {
          return;
        }
        if (!want || !want.cols || !want.rows) return;
        const c = want.cols;
        const r = want.rows;
        if (!force && c === sent.c && r === sent.r) return;
        if (sock.sendJSON({ t: 'r', c, r })) sent = { c, r };
      }

      const sendInput = (d: string) => {
        if (!feed.replaying) sock.sendJSON({ t: 'i', d });
      };
      // focused, or pressed (a view already focused in a tab left and come back to gets no focus
      // event): the hub gives this view's size to the terminal
      const onFocus = () => sock.sendJSON({ t: 'f' });
      term.textarea?.addEventListener('focus', onFocus);
      el.addEventListener('pointerdown', onFocus, true);
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

      // A console kept in tmux on a server shows tmux's alternate screen, which has no scrollback
      // here, so xterm turned the wheel into ↑/↓ and a shell paged its command history. Its lines
      // go to the hub instead, which scrolls tmux's own history (Session.Scroll); a program there
      // that asks for the mouse still gets it from xterm.
      let wheelLines = 0;
      let wheelTimer: number | undefined;
      const flushWheel = () => {
        wheelTimer = undefined;
        const n = Math.trunc(wheelLines);
        if (!n) return;
        wheelLines -= n;
        sock.sendJSON({ t: 's', n });
      };
      term.attachCustomWheelEventHandler((ev) => {
        if (!store.getState().sessions[sessionId]?.tmuxName) return true;
        if (term.buffer.active.type !== 'alternate' || term.modes.mouseTrackingMode !== 'none') return true;
        ev.preventDefault();
        const rowPx = (term.options.fontSize ?? FONT_SIZE) * (term.options.lineHeight ?? 1);
        wheelLines += ev.deltaMode === 1 ? ev.deltaY : ev.deltaMode === 2 ? ev.deltaY * term.rows : ev.deltaY / rowPx;
        wheelTimer ??= window.setTimeout(flushWheel, 40);
        return false;
      });

      const attachFiles = async (files: File[]) => {
        const sent = await sendPasted(files, (f) => api.attachToSession(sessionId, f), { forAgent: true });
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
      // a file dropped on the console is a pasted one
      const undrop = watchFileDrop(
        el,
        (files) => {
          term.focus();
          void attachFiles(files);
        },
        { label: 'Drop to send to this console' },
      );

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
      // WebGL or the DOM renderer (webglPool.ts), decided before the first fit
      const unpool = registerTerminal(term, el, () => fitAndSend(), () => fit.proposeDimensions());
      fitAndSend();
      live.current = { term, refit: () => fitAndSend() };

      return () => {
        live.current = null;
        unpool();
        unregister();
        window.clearTimeout(resizeTimer);
        window.clearTimeout(wheelTimer);
        el.removeEventListener('paste', onPaste, true);
        term.textarea?.removeEventListener('focus', onFocus);
        el.removeEventListener('pointerdown', onFocus, true);
        undrop();
        ro.disconnect();
        dataSub.dispose();
        binSub.dispose();
        sock.close();
        feed.dispose();
        disposeTerminal(term);
      };
    }
  }, [sessionId, containerRef]);

  return connState;
}
