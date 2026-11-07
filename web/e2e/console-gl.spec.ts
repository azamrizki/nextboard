import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Locator, Page } from '@playwright/test';
import { expect, test } from './hub';

// Consoles drawn by the GPU (useTerminal.ts tryWebgl). xterm's WebGL renderer sizes its canvas by
// the device pixels the browser reports, and under an emulated pixel ratio (Chrome's device mode,
// these test browsers) those are CSS pixels: the text fell outside the canvas and a console showed
// nothing. Such a console falls back to the DOM renderer. The test browser gets a GPU it would
// otherwise skip as software.
test.use({ launchOptions: { args: ['--use-angle=swiftshader', '--enable-unsafe-swiftshader', '--ignore-gpu-blocklist'] } });

async function gpu(page: Page) {
  await page.addInitScript(() => {
    const types = [WebGLRenderingContext, (window as { WebGL2RenderingContext?: typeof WebGLRenderingContext }).WebGL2RenderingContext];
    for (const C of types) {
      if (!C) continue;
      const get = C.prototype.getParameter;
      // UNMASKED_RENDERER_WEBGL, RENDERER: a phone's GPU, not SwiftShader
      C.prototype.getParameter = function (this: WebGLRenderingContext, p: number) {
        return p === 0x9246 || p === 0x1f01 ? 'Adreno (TM) 650' : get.call(this, p);
      };
    }
  });
}

/** The fake agent's prompt is on the screen: drawn by the DOM renderer, or by the GPU in a canvas of the right size. */
async function shows(term: Locator, ratio: number) {
  const screen = term.locator('.xterm-screen');
  if (ratio > 1) {
    await expect(term.locator('.xterm-rows')).toContainText('>');
    await expect(screen.locator('canvas')).toHaveCount(0);
    return;
  }
  await expect(screen.locator('canvas')).toHaveCount(2);
  await expect(term.locator('.xterm-rows')).toHaveCount(0);
  for (const c of await screen.locator('canvas').all()) {
    const [w, css] = await c.evaluate((el: HTMLCanvasElement) => [el.width, parseFloat(el.style.width)]);
    expect(Math.abs(w - css)).toBeLessThanOrEqual(2);
  }
}

test('a console shows its text at any pixel ratio, on the board and in the editor', async ({ page, hub }, info) => {
  await gpu(page);
  const ratio = info.project.use.deviceScaleFactor ?? 1;
  const p = await hub.project('Pixels');
  writeFileSync(join(p.path, 'README.md'), '# Pixels\n');
  const c = await hub.console(p.wid);
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_edit000001', kind: 'editor', x: 0, y: 1400, w: 1000, h: 640, data: {} } }]);

  await page.goto(`/p/${p.id}/w/${p.wid}?card=${c.node}`);
  await shows(page.locator(`.react-flow__node[data-id="${c.node}"] .xterm`), ratio);

  // an agent in an editor tab
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_edit000001`);
  await expect(page.locator('.wb-tab', { hasText: 'README.md' })).toBeVisible({ timeout: 15_000 });
  // on a phone the side bar may still lie over the editor
  const scrim = page.getByRole('button', { name: 'Close the side bar' });
  if (await scrim.isVisible()) await scrim.dispatchEvent('click');
  await expect(scrim).toHaveCount(0);
  await page.getByRole('button', { name: 'Open an agent in a new tab' }).click();
  await page.getByRole('menuitem', { name: 'Fake CLI' }).click();
  const pane = page.locator('.wb-pane-tab .xterm');
  await shows(pane, ratio);

  // Chrome's device mode turned on with the page open: a tablet's screen and pixel ratio
  if (ratio > 1) return;
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setDeviceMetricsOverride', { width: 800, height: 1280, deviceScaleFactor: 2, mobile: true });
  await shows(pane, 2);
});
