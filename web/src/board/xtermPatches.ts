// Our own fixes for xterm.js bugs that are still open in the versions we ship (@xterm/xterm 6.0.0,
// @xterm/addon-webgl 0.19.0). Like followBoardZoom in useTerminal.ts they reach private members
// through instances: every name is checked first, and when an upgrade renamed one that patch is
// skipped with one console.warn saying what goes wrong again. Each patch names its upstream issue
// or PR: delete it once a release we use has the fix. Prototypes are patched once (marked with a
// symbol); what differs per terminal is patched on its instances.
//
// Left alone: #6291, a theme change builds a new texture atlas per terminal and drops the old
// one's canvases late. Themes change rarely.

/* eslint-disable @typescript-eslint/no-explicit-any */
import type { Terminal } from '@xterm/xterm';
import type { WebglAddon } from '@xterm/addon-webgl';

type Any = any;

const PATCHED = Symbol('nb.patched');
const LAYOUT = Symbol('nb.atlasLayout');
const SEEN = Symbol('nb.atlasSeen');
const UPLOADED = Symbol('nb.uploadedPages');

const warned = new Set<string>();
function skip(lost: string): false {
  if (!warned.has(lost)) {
    warned.add(lost);
    console.warn(`xterm internals changed: ${lost}`);
  }
  return false;
}

const isFn = (f: unknown): f is (...a: Any[]) => Any => typeof f === 'function';
const core = (term: Terminal): Any => (term as Any)._core;

// ---- #6015: one cell grid for both renderers ----

/** The cell of the WebGL renderer (WebglRenderer._updateDimensions): whole device pixels wide and high. */
export function webglCell(charWidth: number, charHeight: number, dpr: number, lineHeight: number, letterSpacing: number) {
  const deviceCharWidth = Math.floor(charWidth * dpr);
  const deviceCellWidth = deviceCharWidth + Math.round(letterSpacing);
  const deviceCellHeight = Math.floor(Math.ceil(charHeight * dpr) * lineHeight);
  return { deviceCharWidth, deviceCellWidth, deviceCellHeight, cssWidth: deviceCellWidth / dpr, cssHeight: deviceCellHeight / dpr };
}

/**
 * #6015 (open): WebGL floors a cell to whole device pixels, the DOM renderer keeps the fraction and
 * then rounds the whole row to CSS pixels. With a 7.8 px glyph a 720 px tile is 100 columns in one
 * and 90 in the other, so a renderer swap resized the PTY and the agent redrew its screen. The DOM renderer's sizes are
 * set to WebGL's here; its letter-spacing (_setDefaultSpacing) then lays the glyphs on that grid.
 * Flooring the measured char width alone is not enough: the DOM renderer still derives the cell
 * from the rounded row, which gave other rows or columns in about 0.5 % of tile sizes, and in a
 * third to half of them at ratios 1.25 to 1.75 with JetBrains Mono.
 * Delete when the renderers agree upstream.
 */
function patchDomGrid(renderer: Any): boolean {
  const proto = renderer && Object.getPrototypeOf(renderer);
  if (proto?.[PATCHED]) return true;
  const d = renderer?.dimensions;
  if (
    !proto ||
    !isFn(proto._updateDimensions) ||
    !isFn(renderer.handleCharSizeChanged) ||
    !d?.device?.cell ||
    !d.css?.cell ||
    !d.css.canvas ||
    !Array.isArray(renderer._rowElements) ||
    !renderer._screenElement?.style ||
    typeof renderer._coreBrowserService?.dpr !== 'number' ||
    typeof renderer._bufferService?.cols !== 'number' ||
    typeof renderer._charSizeService?.width !== 'number' ||
    !renderer._optionsService?.rawOptions
  ) {
    return skip('a console changes its columns when it moves between the GPU and the page renderer');
  }
  const update = proto._updateDimensions;
  proto._updateDimensions = function (this: Any) {
    update.call(this);
    const chars = this._charSizeService;
    if (!(chars.width > 0) || !(chars.height > 0)) return;
    const dpr = this._coreBrowserService.dpr;
    const opts = this._optionsService.rawOptions;
    const cell = webglCell(chars.width, chars.height, dpr, opts.lineHeight, opts.letterSpacing);
    const dims = this.dimensions;
    dims.device.char.width = cell.deviceCharWidth;
    dims.device.cell.width = cell.deviceCellWidth;
    dims.device.cell.height = cell.deviceCellHeight;
    dims.device.canvas.width = cell.deviceCellWidth * this._bufferService.cols;
    dims.device.canvas.height = cell.deviceCellHeight * this._bufferService.rows;
    dims.css.canvas.width = Math.round(dims.device.canvas.width / dpr);
    dims.css.canvas.height = Math.round(dims.device.canvas.height / dpr);
    dims.css.cell.width = cell.cssWidth;
    dims.css.cell.height = cell.cssHeight;
    // what the original set from its own numbers
    for (const row of this._rowElements as HTMLElement[]) {
      row.style.width = `${dims.css.canvas.width}px`;
      row.style.height = `${dims.css.cell.height}px`;
      row.style.lineHeight = `${dims.css.cell.height}px`;
    }
    this._screenElement.style.width = `${dims.css.canvas.width}px`;
    this._screenElement.style.height = `${dims.css.canvas.height}px`;
  };
  proto[PATCHED] = true;
  return true;
}

// ---- #6239: no blank frame when a renderer is set or cleared while paused ----

const RENDER = Symbol('nb.renderNow');
// set while the WebGL renderer clears its canvas for a new size (see patchWebglRenderer)
let canvasCleared = false;

/**
 * #6239 / PR #6240 (open): xterm pauses an off-screen terminal and keeps every refresh for when it
 * shows again, a frame late. A renderer set while paused (the pool swaps off-screen consoles) was
 * then blank for a frame when panned back; so was a WebGL canvas cleared by a resize. Render those
 * right away instead. Delete with #6240.
 */
function patchRenderService(term: Terminal): void {
  const rs = core(term)?._renderService;
  if (rs?.[PATCHED]) return;
  if (
    !rs ||
    !isFn(rs.setRenderer) ||
    !isFn(rs.refreshRows) ||
    !isFn(rs.handleResize) ||
    !isFn(rs._renderRows) ||
    typeof rs._isPaused !== 'boolean' ||
    typeof rs._rowCount !== 'number' ||
    !isFn(rs._pausedResizeTask?.flush) ||
    !isFn(rs._pausedResizeTask?.set) ||
    !rs._renderer
  ) {
    skip('an off-screen console can show blank for a frame after its renderer changed');
    return;
  }
  rs[PATCHED] = true;
  // A render asks for redraws of its own (the selection after a new renderer): those wait for the
  // next frame as before, rendering them here again would never end.
  let rendering = false;
  const render = (start: number, end: number) => {
    if (rendering) return;
    rendering = true;
    try {
      rs._renderRows(start, end);
    } finally {
      rendering = false;
    }
  };
  rs[RENDER] = render;
  // fixed upstream when refreshRows itself flushes a paused resize
  if (String(rs.refreshRows).includes('_pausedResizeTask')) return;
  // Inside the paused resize task a redraw must only render: flushing the task again would run it
  // again (#6240 overflowed the stack there).
  let resizing = false;
  const inResize = (run: () => void) => {
    const was = resizing;
    resizing = true;
    try {
      run();
    } finally {
      resizing = was;
    }
  };
  // the refresh is still kept for when it shows (_needsFullRefresh): this only fills it in now
  const renderPaused = (start: number, end: number) => {
    if (rendering) return;
    if (!resizing) inResize(() => rs._pausedResizeTask.flush());
    render(start, end);
  };
  const setRenderer = rs.setRenderer.bind(rs);
  rs.setRenderer = (renderer: Any) => {
    if (renderer?._glyphRenderer) patchWebglRenderer(renderer);
    setRenderer(renderer);
    if (rs._isPaused && rs._renderer.value) renderPaused(0, rs._rowCount - 1);
  };
  const refreshRows = rs.refreshRows.bind(rs);
  rs.refreshRows = (start: number, end: number, redrawOnly = false) => {
    // only the redraw the clear itself asks for; what rendering it sets off is ordinary
    const cleared = canvasCleared;
    canvasCleared = false;
    refreshRows(start, end, redrawOnly);
    if (rs._isPaused && (redrawOnly || cleared)) renderPaused(start, end);
    // upstream master draws a cleared canvas at once: on the next frame it had shown blank once
    else if (cleared) render(start, end);
  };
  const handleResize = rs.handleResize.bind(rs);
  rs.handleResize = (cols: number, rows: number) => {
    handleResize(cols, rows);
    // the deferred resize clears a WebGL canvas: draw it again right after
    if (rs._isPaused && rs._renderer.value) {
      rs._pausedResizeTask.set(() => {
        inResize(() => {
          rs._renderer.value?.handleResize(cols, rows);
          render(0, rs._rowCount - 1);
        });
      });
    }
  };
}

/** Draws the terminal now, not on the next frame: a renderer swap shows no frame without text. */
export function renderNow(term: Terminal): void {
  const rs = core(term)?._renderService;
  if (!isFn(rs?._renderRows) || typeof rs._rowCount !== 'number' || !rs._renderer?.value) return;
  try {
    if (rs[RENDER]) rs[RENDER](0, rs._rowCount - 1);
    else rs._renderRows(0, rs._rowCount - 1);
  } catch {
    /* the next frame draws it */
  }
}

// ---- WebGL renderer internals, patched the first time one is set ----

/** #6038 / PR #6042: the per-atlas layout version, bumped where 0.19 sets a flag nobody resets. */
function patchAtlas(atlas: Any): void {
  if (!atlas || typeof atlas[LAYOUT] === 'number' || atlas[LAYOUT] === false) return;
  const proto = Object.getPrototypeOf(atlas);
  if (proto && !proto[PATCHED]) {
    proto[PATCHED] = true;
    // #6187 (open): merged pages grow to MAX_TEXTURE_SIZE², 256 MiB of GPU memory per context at
    // 8192². 4096 is xterm's own FORCED_MAX_TEXTURE_SIZE. Delete with #6187.
    const C = atlas.constructor;
    if (C && typeof C.maxTextureSize === 'number') C.maxTextureSize = Math.min(C.maxTextureSize, 4096);
    else skip('merged glyph pages can take up to 256 MiB of GPU memory');
    // #6014 / PR #6055: clearTexture() on a shared atlas left the other terminals drawing from
    // wiped pages. Bump the layout after the early return, so each of them rebuilds. Delete with #6055.
    if (isFn(proto.clearTexture)) {
      const clear = proto.clearTexture;
      proto.clearTexture = function (this: Any) {
        const before = this._pages?.[0]?.version;
        clear.call(this);
        if (typeof this[LAYOUT] === 'number' && this._pages?.[0]?.version !== before) this[LAYOUT]++;
      };
    } else skip('clearing the glyph atlas of one console garbles the others');
  }
  const flag = Object.getOwnPropertyDescriptor(atlas, '_requestClearModel');
  if (!flag || !('value' in flag) || !isFn(atlas.beginFrame)) {
    atlas[LAYOUT] = false;
    skip('every console sharing a glyph atlas redraws in full on every frame after a page merge');
    return;
  }
  // an atlas that merged before it was seen here: everyone rebuilds once
  atlas[LAYOUT] = flag.value ? 1 : 0;
  Object.defineProperty(atlas, '_requestClearModel', {
    configurable: true,
    get: () => false,
    set(this: Any, v: boolean) {
      if (v) this[LAYOUT]++;
    },
  });
}

function patchGlyphRenderer(glyphs: Any): void {
  const proto = glyphs && Object.getPrototypeOf(glyphs);
  if (!proto || proto[PATCHED]) return;
  proto[PATCHED] = true;
  const textures = glyphs._atlasTextures;
  if (!Array.isArray(textures) || !textures[0]?.texture || !('version' in textures[0]) || !glyphs._gl) {
    skip('glyph pages are kept in GPU memory longer than needed');
    return;
  }

  // #6038 / PR #6042 and its 0.19 form: after one page merge the atlas asked every terminal sharing
  // it to rebuild its whole model on every frame, forever. Each renderer now rebuilds once per
  // layout change it has not seen. Delete with #6042.
  if (isFn(proto.beginFrame) && isFn(proto.setAtlas)) {
    const begin = proto.beginFrame;
    proto.beginFrame = function (this: Any) {
      const atlas = this._atlas;
      if (!atlas) return true;
      patchAtlas(atlas);
      const v = atlas[LAYOUT];
      if (typeof v !== 'number') return begin.call(this);
      if (this[SEEN] === v) return false;
      this[SEEN] = v;
      return true;
    };
    const setAtlas = proto.setAtlas;
    proto.setAtlas = function (this: Any, atlas: Any) {
      patchAtlas(atlas);
      setAtlas.call(this, atlas);
      this[SEEN] = -1;
    };
  } else skip('every console sharing a glyph atlas redraws in full on every frame after a page merge');

  // #6306 / #6307 (open): when the atlas has fewer pages after a merge, the slots past its end kept
  // their old page in GPU memory, and drew stale glyphs if a new page later came with the same
  // version. Give them back the constructor's 1×1 placeholder. Delete with #6306.
  if (isFn(proto.render)) {
    const render = proto.render;
    const red = new Uint8Array([255, 0, 0, 255]);
    proto.render = function (this: Any, model: Any) {
      render.call(this, model);
      const pages = this._atlas?.pages?.length;
      if (typeof pages !== 'number') return;
      const had: number = this[UPLOADED] ?? 0;
      if (pages < had) {
        const gl = this._gl;
        for (let i = pages; i < had; i++) {
          gl.activeTexture(gl.TEXTURE0 + i);
          gl.bindTexture(gl.TEXTURE_2D, this._atlasTextures[i].texture);
          gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, 1, 1, 0, gl.RGBA, gl.UNSIGNED_BYTE, red);
          this._atlasTextures[i].version = -1;
        }
      }
      this[UPLOADED] = pages;
    };
  } else skip('glyph pages are kept in GPU memory longer than needed');

  // #5987 (merged after 0.19): a mipmap chain for every page upload, a third more texture memory,
  // slower uploads and GL errors from ANGLE on some Linux drivers, for glyphs that are only ever
  // drawn 1:1 (board zoom scales the finished canvas). The default min filter needs mipmaps, so
  // LINEAR goes with it. Delete with the next addon release.
  const bind = proto._bindAtlasPageTexture;
  if (isFn(bind) && /generateMipmap/.test(String(bind)) && /texImage2D/.test(String(bind))) {
    proto._bindAtlasPageTexture = function (this: Any, gl: WebGL2RenderingContext, atlas: Any, i: number) {
      gl.activeTexture(gl.TEXTURE0 + i);
      gl.bindTexture(gl.TEXTURE_2D, this._atlasTextures[i].texture);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, atlas.pages[i].canvas);
      this._atlasTextures[i].version = atlas.pages[i].version;
    };
  } else if (isFn(bind) && /generateMipmap/.test(String(bind))) skip('glyph pages take a third more GPU memory');
}

/** Patches the prototypes behind a WebGL renderer, before it first draws. Safe to call again. */
function patchWebglRenderer(renderer: Any): void {
  const proto = Object.getPrototypeOf(renderer);
  if (proto && !proto[PATCHED]) {
    proto[PATCHED] = true;
    const resize = proto._setCanvasDevicePixelDimensions;
    if (isFn(resize) && !/sync/.test(String(resize))) {
      // see canvasCleared in patchRenderService
      proto._setCanvasDevicePixelDimensions = function (this: Any, w: number, h: number) {
        const was = canvasCleared;
        canvasCleared = true;
        try {
          resize.call(this, w, h);
        } finally {
          canvasCleared = was;
        }
      };
    }
  }
  const glyphs = renderer._glyphRenderer?.value;
  if (glyphs) patchGlyphRenderer(glyphs);
  else skip('glyph pages are kept in GPU memory longer than needed');
  // its atlas was set before the prototypes were patched
  patchAtlas(renderer._charAtlas);
}

/** For a renderer service this module could not wrap: patch the addon's renderer after loading it. */
export function patchWebgl(addon: WebglAddon): void {
  const renderer = (addon as Any)._renderer;
  if (renderer?._glyphRenderer) patchWebglRenderer(renderer);
}

// ---- lifetime ----

/**
 * Patches a terminal right after term.open(): the paused-render fix, and (when consoles may use
 * WebGL) the shared cell grid. Gives back whether renderers can be swapped without changing the
 * terminal's columns and rows.
 */
export function prepareTerminal(term: Terminal, grid: boolean): boolean {
  patchRenderService(term);
  if (!grid) return false;
  const renderer = core(term)?._renderService?._renderer?.value;
  const proto = renderer && Object.getPrototypeOf(renderer);
  const before = !!proto?.[PATCHED];
  if (!patchDomGrid(renderer)) return false;
  // the first terminal's renderer was made before the patch
  if (!before) renderer.handleCharSizeChanged();
  return true;
}

/**
 * #5817 / #5818 (fixed upstream after 6.0.0) and #6068 / PR #6069 (merged after 0.19): frees a
 * WebGL addon for real. Its cursor-blink timer was never disposed and kept the renderer, and with it
 * the terminal, alive for good once the console had been focused; and disposing did not free the
 * context, which counted against the browser's cap until garbage collection. Delete with the next
 * addon release.
 */
export function releaseWebgl(addon: WebglAddon): void {
  const r = (addon as Any)._renderer;
  const gl: WebGL2RenderingContext | undefined = r?._gl;
  const blink = r?._cursorBlinkStateManager;
  if (isFn(blink?.clear)) blink.clear();
  else if (r) skip('a closed console with a blinking cursor stays in memory');
  // Not reported upstream yet (found 2026-11-15): a shared atlas draws each new glyph on a scratch
  // canvas it moves into the element of the terminal drawing, so a closed console's whole DOM, and
  // through xterm's listeners the terminal, stayed alive as long as another console used the
  // atlas. The next glyph puts it back where it is drawn. Delete when the atlas stops doing that.
  const atlas = r?._charAtlas;
  if (atlas) {
    const scratch = atlas._tmpCanvas;
    if (scratch instanceof HTMLCanvasElement) {
      if (r._terminal?.element?.contains(scratch)) scratch.remove();
    } else skip('a closed console can stay in memory while others share its glyph atlas');
  }
  // the 3 s wait for a restore after a context loss (#5930)
  if (r?._contextRestorationTimeout !== undefined) {
    clearTimeout(r._contextRestorationTimeout);
    r._contextRestorationTimeout = undefined;
  }
  try {
    addon.dispose();
  } catch {
    /* already gone */
  }
  if (!isFn(gl?.getExtension)) {
    if (r) skip('a closed console holds its GPU context until garbage collection');
    return;
  }
  try {
    if (!gl.isContextLost()) gl.getExtension('WEBGL_lose_context')?.loseContext();
  } catch {
    /* ignore */
  }
}

/**
 * #6257 (open): terminals sharing an atlas stayed blank after the browser restored a lost context.
 * Never let it restore: on a loss `lost` runs (next microtask) to release this addon, and the pool
 * decides later whether the console gets a fresh one. Gives back what stops watching.
 */
export function watchContextLoss(addon: WebglAddon, lost: () => void): () => void {
  const r = (addon as Any)._renderer;
  const canvas: HTMLCanvasElement | undefined = r?._canvas;
  if (!(canvas instanceof HTMLCanvasElement)) {
    skip('a console stays blank after the browser restores its GPU context');
    const sub = addon.onContextLoss(lost);
    return () => sub.dispose();
  }
  let done = false;
  const onLost = (e: Event) => {
    // before xterm's own listener, which asks the browser for a restore
    e.stopImmediatePropagation();
    if (done) return;
    done = true;
    queueMicrotask(lost);
  };
  canvas.addEventListener('webglcontextlost', onLost, { capture: true });
  // a browser that ran xterm's listener first restores later: lose it again, this canvas is done
  const gl: WebGL2RenderingContext | undefined = r._gl;
  canvas.addEventListener('webglcontextrestored', () => gl?.getExtension('WEBGL_lose_context')?.loseContext(), { once: true });
  return () => canvas.removeEventListener('webglcontextlost', onLost, { capture: true });
}

/** #5817 / #5818: what term.dispose() leaves running. blur() stops the cursor-blink timer. */
export function disposeTerminal(term: Terminal): void {
  try {
    term.blur();
  } catch {
    /* not open */
  }
  const task = core(term)?._renderService?._pausedResizeTask;
  if (isFn(task?.dispose)) task.dispose();
  else if (isFn(task?._queue?.clear)) task._queue.clear();
  else if (task) skip('a closed console can stay in memory until the browser is idle');
  term.dispose();
}
