import { readFileSync, readdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test } from './hub';

// Files from the computer go up as raw bytes (lib/pack.ts): text gzip-compressed by the browser,
// pictures as they are. What lands is the file, byte for byte. Over plain HTTP, as the hub is.
test('pasted files go up raw, text compressed, and land as they were', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light', 'one board is enough');
  const p = await hub.project('Upload');
  writeFileSync(join(p.path, 'readme.txt'), 'hi\n');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_edit000002', kind: 'editor', x: 0, y: 0, w: 1200, h: 720, data: {} } }]);
  // a Blob body is out of sight of the network log: the page says what it sent
  await page.addInitScript(() => {
    const w = window as unknown as { __sent: unknown[] };
    w.__sent = [];
    const orig = window.fetch.bind(window);
    window.fetch = (input, init) => {
      const url = String(input);
      if (init?.method === 'PUT' && url.includes('/fs/write') && init.body instanceof Blob) {
        const h = (init.headers ?? {}) as Record<string, string>;
        w.__sent.push({ url, type: h['Content-Type'], encoding: h['Content-Encoding'], bytes: init.body.size });
      }
      return orig(input, init);
    };
  });
  type Sent = { url: string; type?: string; encoding?: string; bytes: number };
  const sent = () => page.evaluate(() => (window as unknown as { __sent: Sent[] }).__sent);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_edit000002`);
  const side = page.locator('.wb-side');
  await side.getByRole('treeitem', { name: 'readme.txt' }).click({ timeout: 20_000 });

  const log = '2026-11-15 12:00:00 INFO request served in 3 ms\n'.repeat(4000);
  const png = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, ...Array.from({ length: 40000 }, (_, i) => (i * 7919) % 251)];
  await page.evaluate(
    ([text, bytes]) => {
      const dt = new DataTransfer();
      dt.items.add(new File([text as string], 'server.log', { type: 'text/plain' }));
      dt.items.add(new File([new Uint8Array(bytes as number[])], 'shot.png', { type: 'image/png' }));
      document.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    },
    [log, png] as const,
  );
  await expect.poll(() => readdirSync(p.path).sort().join(','), { timeout: 15_000 }).toBe('readme.txt,server.log,shot.png');
  expect(readFileSync(join(p.path, 'server.log'), 'utf8')).toBe(log);
  expect([...readFileSync(join(p.path, 'shot.png'))]).toEqual(png);

  const all = await sent();
  expect(all).toHaveLength(2);
  const text = all.find((s) => s.url.includes('server.log'))!;
  const pic = all.find((s) => s.url.includes('shot.png'))!;
  expect(text.type).toBe('application/octet-stream');
  expect(text.encoding).toBe('gzip');
  expect(text.bytes).toBeGreaterThan(0);
  expect(text.bytes).toBeLessThan(log.length / 10);
  expect(text.url).toContain('unique=1');
  expect(pic.encoding).toBeUndefined();
  expect(pic.bytes).toBe(png.length);
});
