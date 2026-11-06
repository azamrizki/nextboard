import { expect, test } from './hub';

// NextBoard as an app (DESIGN.md §3.17): a manifest Chrome can install from, an Install button
// while the browser offers it, and over plain HTTP a warning of what the browser gives instead.

/** What Chrome hands the page when it offers the install, with a dialog that says `outcome`. */
const offer = (outcome: 'accepted' | 'dismissed') => {
  const w = window as unknown as { prompted: number };
  w.prompted = 0;
  const e = Object.assign(new Event('beforeinstallprompt'), {
    prompt: async () => {
      w.prompted++;
    },
    userChoice: Promise.resolve({ outcome }),
  });
  window.dispatchEvent(e);
};

test('Chrome can install the hub: a manifest with its icons, and nothing missing', async ({ page, request }, info) => {
  test.skip(info.project.name !== 'light', 'once');
  const m = await request.get('/manifest.json');
  expect(m.headers()['content-type']).toContain('application/json');
  const manifest = await m.json();
  expect(manifest).toMatchObject({ name: 'NextBoard', start_url: '/', display: 'standalone' });
  for (const icon of manifest.icons) {
    const r = await request.get(icon.src);
    expect(r.ok(), icon.src).toBe(true);
    expect(r.headers()['content-type']).toBe(icon.type);
  }

  await page.goto('/');
  await expect(page.locator('.app-header')).toBeVisible();
  const cdp = await page.context().newCDPSession(page);
  const parsed = await cdp.send('Page.getAppManifest');
  expect(parsed.errors).toEqual([]);
  // this computer counts as secure: the install is offered here as over HTTPS
  const { installabilityErrors } = await cdp.send('Page.getInstallabilityErrors');
  expect(installabilityErrors).toEqual([]);
});

test('Install app: the browser’s offer shows a button, and an installed app says so', async ({ page }, info) => {
  test.skip(!['light', 'phone'].includes(info.project.name), 'desktop and phone');
  await page.goto('/settings/app');
  await expect(page.getByRole('heading', { name: 'Install app' })).toBeVisible();
  // no offer yet: the browser's own menu
  await expect(page.getByText('Install from the browser’s menu')).toBeVisible();

  await page.evaluate(offer, 'dismissed');
  await page.getByRole('button', { name: 'Install', exact: true }).click();
  await expect.poll(() => page.evaluate(() => (window as unknown as { prompted: number }).prompted)).toBe(1);
  // turned down: the offer is spent, and the pane says the browser may ask again
  await expect(page.getByRole('button', { name: 'Install', exact: true })).toHaveCount(0);

  await page.evaluate(offer, 'accepted');
  if (info.project.name === 'phone') {
    // a phone's "…" has it too, while it is offered
    await page.getByRole('button', { name: 'More', exact: true }).tap();
    await page.getByRole('menuitem', { name: 'Install app' }).tap();
  } else {
    await page.getByRole('button', { name: 'Install', exact: true }).click();
  }
  await expect.poll(() => page.evaluate(() => (window as unknown as { prompted: number }).prompted)).toBe(1);
  await page.evaluate(() => window.dispatchEvent(new Event('appinstalled')));
  await expect(page.getByText('Installed', { exact: true })).toBeVisible();
});

test('over plain HTTP the pane warns that the browser only adds a shortcut', async ({ page }, info) => {
  test.skip(!['dark', 'phone'].includes(info.project.name), 'desktop and phone');
  // the test hub is on this computer, which counts as secure: be a hub reached over the network
  await page.addInitScript(() => Object.defineProperty(window, 'isSecureContext', { value: false }));
  await page.goto('/settings/app');
  const note = page.getByRole('note');
  await expect(note).toContainText('plain HTTP');
  await expect(note).toContainText(new URL(page.url()).origin);
  await expect(page.getByText('Insecure origins treated as secure')).toBeVisible();
  // the warning fits a phone: nothing runs off the side
  expect(await page.evaluate(() => Math.max(document.documentElement.scrollWidth, window.innerWidth))).toBeLessThanOrEqual(page.viewportSize()!.width);
});
