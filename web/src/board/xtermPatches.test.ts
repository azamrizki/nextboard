import { describe, expect, it } from 'vitest';
import { webglCell } from './xtermPatches';

// The two renderers' sizes as @xterm/xterm 6.0.0 DomRenderer._updateDimensions and
// @xterm/addon-webgl 0.19.0 WebglRenderer._updateDimensions compute them, and what the fit addon
// makes of them (cols = floor(width / css cell), rows the same).
const LINE_HEIGHT = 1.15;
function webgl(w: number, h: number, dpr: number) {
  const cellW = Math.floor(w * dpr);
  const cellH = Math.floor(Math.ceil(h * dpr) * LINE_HEIGHT);
  return { w: cellW / dpr, h: cellH / dpr };
}
function dom(w: number, h: number, dpr: number, cols: number, rows: number) {
  const cellW = w * dpr;
  const cellH = Math.floor(Math.ceil(h * dpr) * LINE_HEIGHT);
  return { w: Math.round((cellW * cols) / dpr) / cols, h: Math.round((cellH * rows) / dpr) / rows };
}
function patchedDom(w: number, h: number, dpr: number) {
  const c = webglCell(w, h, dpr, LINE_HEIGHT, 0);
  return { w: c.cssWidth, h: c.cssHeight };
}
const fit = (cell: { w: number; h: number }, width: number, height: number) => [Math.max(2, Math.floor(width / cell.w)), Math.max(1, Math.floor(height / cell.h))];

const RATIOS = [1, 1.25, 1.5, 1.75, 2, 2.75, 3];
// JetBrains Mono is 0.6 em wide and 1.32 em high; the text scales give 11 to 26 px; 7.8267 is a fallback font
const FONTS = [11, 13, 15, 17, 20, 23, 26].map((px) => ({ w: Math.fround(0.6 * px), h: 1.32 * px })).concat([{ w: 7.8267, h: 15.6 }]);

describe('one cell grid for both renderers (#6015)', () => {
  it('gives the DOM renderer the WebGL renderer’s cell, to the last bit', () => {
    for (const dpr of RATIOS)
      for (const f of FONTS) {
        const a = patchedDom(f.w, f.h, dpr);
        const b = webgl(f.w, f.h, dpr);
        expect(a.w).toBe(b.w);
        expect(a.h).toBe(b.h);
        // whole device pixels, without float drift
        expect(Number.isInteger(Math.round(a.w * dpr * 1e9) / 1e9)).toBe(true);
        expect(Math.floor(a.w * dpr + 1e-9)).toBe(Math.floor(f.w * dpr));
      }
  });

  it('keeps columns and rows through a swap at every pixel ratio and text size', () => {
    let checked = 0;
    let unpatchedDiffer = 0;
    for (const dpr of RATIOS)
      for (const f of FONTS)
        for (let width = 200; width < 1800; width += 7)
          for (const height of [150 + (width % 311), 420 + (width % 173)]) {
            const [cols, rows] = fit(webgl(f.w, f.h, dpr), width, height);
            expect(fit(patchedDom(f.w, f.h, dpr), width, height)).toEqual([cols, rows]);
            const [c2, r2] = fit(dom(f.w, f.h, dpr, cols, rows), width, height);
            if (c2 !== cols || r2 !== rows) unpatchedDiffer++;
            checked++;
          }
    // the bug this fixes: xterm's own DOM sizes give other columns or rows for most of these
    expect(unpatchedDiffer / checked).toBeGreaterThan(0.5);
  });

  it('counts letter spacing in whole device pixels, like WebGL', () => {
    const c = webglCell(7.8, 17.16, 1.5, 1.15, 1.4);
    expect(c.deviceCellWidth).toBe(Math.floor(7.8 * 1.5) + 1);
    expect(c.cssWidth).toBe(c.deviceCellWidth / 1.5);
  });
});
