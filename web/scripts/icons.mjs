// Renders the app's PNG icons (public/icons) from the two SVGs beside them, with the
// Chromium that the end-to-end tests use: `node scripts/icons.mjs`. The maskable one fills its
// square, with the mark inside the circle a launcher may cut it to.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { chromium } from '@playwright/test';

const dir = join(dirname(fileURLToPath(import.meta.url)), '..', 'public', 'icons');
const out = [
  ['icon.svg', 192, 'icon-192.png'],
  ['icon.svg', 512, 'icon-512.png'],
  ['maskable.svg', 512, 'maskable-512.png'],
  ['maskable.svg', 180, 'apple-touch-icon.png'],
];

const browser = await chromium.launch();
const page = await browser.newPage();
for (const [svg, size, png] of out) {
  await page.setViewportSize({ width: size, height: size });
  const src = `data:image/svg+xml;base64,${readFileSync(join(dir, svg)).toString('base64')}`;
  await page.setContent(`<html><body style="margin:0;background:transparent"><img src="${src}" width="${size}" height="${size}" style="display:block"></body></html>`);
  await page.locator('img').evaluate((img) => img.decode());
  await page.screenshot({ path: join(dir, png), omitBackground: true });
}
await browser.close();
