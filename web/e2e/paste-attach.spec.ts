import { chmodSync, rmSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import type { Locator, Page } from '@playwright/test';
import { expect, test } from './hub';

// What a paste into a NextCode message attaches (board/files.ts clipboardFiles, forAgent): a
// picture with a caption beside it, as Telegram on a Mac copies it, is the picture; a picture
// agents cannot read (a Mac's TIFF, a BMP) goes up as a PNG; a spreadsheet's cells with a
// picture of them stay text; a paste with nothing usable says so; a pasted file the browser may
// not read (another app's, as Telegram on a Mac copies a photo) says that, not "network error".

const FAKE = fileURLToPath(new URL('./fake-nextcode.sh', import.meta.url));

type Kind = 'png' | 'bmp';

/** Pastes into el, as a browser would: a picture of kind (if any) and text formats. */
async function paste(input: Locator, picture: Kind | null, data: Record<string, string>) {
  await input.evaluate(
    async (el, { picture, data }) => {
      const dt = new DataTransfer();
      if (picture === 'png') {
        const c = document.createElement('canvas');
        c.width = 40;
        c.height = 30;
        const g = c.getContext('2d')!;
        g.fillStyle = '#3987e5';
        g.fillRect(0, 0, 40, 30);
        const blob = await new Promise<Blob>((done) => c.toBlob((b) => done(b!), 'image/png'));
        dt.items.add(new File([blob], 'image.png', { type: 'image/png' }));
      }
      if (picture === 'bmp') {
        // a 2×2 24-bit BMP
        const w = 2;
        const h = 2;
        const row = 8; // 2 px × 3 bytes, padded to 4
        const buf = new Uint8Array(54 + row * h);
        const v = new DataView(buf.buffer);
        buf[0] = 0x42;
        buf[1] = 0x4d;
        v.setUint32(2, buf.length, true);
        v.setUint32(10, 54, true);
        v.setUint32(14, 40, true);
        v.setInt32(18, w, true);
        v.setInt32(22, h, true);
        v.setUint16(26, 1, true);
        v.setUint16(28, 24, true);
        for (let i = 54; i < buf.length; i++) buf[i] = 0xc0;
        dt.items.add(new File([buf], 'photo.bmp', { type: 'image/bmp' }));
      }
      for (const [t, v] of Object.entries(data)) dt.setData(t, v);
      el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    },
    { picture, data },
  );
}

async function chat(page: Page, hub: { call: <T = unknown>(m: string, p: string, b?: unknown) => Promise<T>; project: (n: string) => Promise<{ id: string; wid: string }> }) {
  await hub.call('PATCH', '/api/settings', { agents: { paths: { local: { claude: FAKE } } } });
  const p = await hub.project('Paste');
  const s = await hub.call<{ id: string; handle: string }>('POST', '/api/sessions', { agent: 'nextcode', workspaceId: p.wid });
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const card = page.locator('.react-flow__node', { has: page.locator('.nc-chat') });
  const input = card.getByRole('combobox', { name: `Message to @${s.handle}` });
  await expect(input).toBeVisible({ timeout: 20_000 });
  return { card, input, s };
}

test('pasted pictures attach, as an agent can read them; cells stay text', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  test.setTimeout(90_000);
  const { card, input, s } = await chat(page, hub);
  const chips = card.locator('.attach-chip');
  try {
    // Telegram on a Mac: the picture with its caption beside it
    await paste(input, 'png', { 'text/plain': 'The new login page' });
    await expect(chips).toHaveCount(1);
    await expect(chips.first()).toHaveAttribute('title', /\/attachments\/.*image\.png$/, { timeout: 15_000 });

    // a picture agents cannot read goes up as a PNG
    await paste(input, 'bmp', {});
    await expect(chips).toHaveCount(2);
    await expect(chips.nth(1)).toHaveAttribute('title', /\/attachments\/.*photo\.png$/, { timeout: 15_000 });

    // a spreadsheet's cells with a picture of them: text, nothing attached
    await paste(input, 'png', { 'text/plain': 'Name\tTotal\nShop\t12' });
    await paste(input, 'png', { 'text/plain': 'Total', 'text/html': '<table><tr><td>Total</td></tr></table>' });
    await expect(chips).toHaveCount(2);

    // nothing this browser can read: said, not silent
    await paste(input, null, {});
    await expect(page.locator('.toast', { hasText: 'Nothing to paste' })).toBeVisible();
    await expect(chips).toHaveCount(2);
  } finally {
    await hub.call('DELETE', `/api/sessions/${s.id}`);
    await hub.call('PATCH', '/api/settings', { agents: { paths: { local: { claude: null } } } });
  }
});

test('a pasted file the browser cannot read says so', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  test.setTimeout(90_000);
  const { card, input, s } = await chat(page, hub);
  await page.evaluate(() => {
    const el = document.createElement('input');
    el.type = 'file';
    el.id = 'clip-file';
    el.hidden = true;
    document.body.append(el);
  });
  /** Pastes a file on disk, as an app puts one on the clipboard, after spoil makes it unreadable. */
  async function pasteUnreadable(name: string, spoil: (path: string) => void) {
    const path = info.outputPath(name);
    writeFileSync(path, Buffer.from([0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 0x4a, 0x46, 0x49, 0x46, 0, 1, 0xff, 0xd9]));
    await page.setInputFiles('#clip-file', path);
    spoil(path);
    await input.evaluate((el) => {
      const dt = new DataTransfer();
      dt.items.add((document.getElementById('clip-file') as HTMLInputElement).files![0]);
      el.dispatchEvent(new ClipboardEvent('paste', { clipboardData: dt, bubbles: true, cancelable: true }));
    });
    return page.locator('.toast', { hasText: `Could not paste ${name}` });
  }
  try {
    // kept from the browser (a Mac keeps an app's own folder from others)
    const denied = await pasteUnreadable('telegram-cloud-photo-size-5-1-x.jpg', (p) => chmodSync(p, 0));
    await expect(denied).toContainText('not able to read this file (NotReadableError)', { timeout: 15_000 });
    // gone since it was copied
    const gone = await pasteUnreadable('telegram-cloud-photo-size-5-2-x.jpg', (p) => rmSync(p));
    await expect(gone).toContainText('no longer there (NotFoundError)', { timeout: 15_000 });
    await expect(page.locator('.toast', { hasText: 'network error' })).toHaveCount(0);
    await expect(card.locator('.attach-chip')).toHaveCount(0);
  } finally {
    rmSync(info.outputPath('telegram-cloud-photo-size-5-1-x.jpg'), { force: true });
    await hub.call('DELETE', `/api/sessions/${s.id}`);
    await hub.call('PATCH', '/api/settings', { agents: { paths: { local: { claude: null } } } });
  }
});
