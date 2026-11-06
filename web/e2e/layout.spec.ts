import type { Page } from '@playwright/test';
import { expect, test } from './hub';

// What every layout has to get right on every page (DESIGN.md §3.0). The same checks run on the
// desktop projects and on the owner's devices (playwright.config.ts): a phone, and a tablet held
// either way.

const PAGES = ['/', '/memory', '/machines', '/devices', '/settings', '/settings/appearance'];

interface Box {
  name: string;
  top: number;
  bottom: number;
  left: number;
  right: number;
}

async function shell(page: Page) {
  // a phone's Chrome widens a page that is too wide and shows it smaller: the screen is the measure
  const W = page.viewportSize()!.width;
  return page.evaluate((W) => {
    const box = (e: Element, name: string) => {
      const r = e.getBoundingClientRect();
      return { name, top: r.top, bottom: r.bottom, left: r.left, right: r.right };
    };
    const header = document.querySelector('.app-header');
    // the header's own controls: the outermost buttons, links and chips, not the tab strip's insides
    const controls = header
      ? [...header.querySelectorAll('button, a, .badge, .status')]
          .filter((e) => !e.closest('.ws-tabs') && !e.parentElement?.closest('button, a'))
          .filter((e) => {
            const r = e.getBoundingClientRect();
            return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== 'hidden';
          })
          .map((e) => box(e, e.getAttribute('aria-label') || e.textContent?.trim() || e.className))
      : [];
    return {
      layout: document.documentElement.dataset.layout,
      input: document.documentElement.dataset.input,
      overflow: Math.max(document.documentElement.scrollWidth, window.innerWidth) - W,
      header: header ? box(header, 'header') : null,
      tabBar: document.querySelector('.tab-bar') ? box(document.querySelector('.tab-bar')!, 'tab bar') : null,
      controls,
      targets: [...document.querySelectorAll('.app-header button, .app-header a, .tab-bar a')]
        .filter((e) => !e.closest('.ws-tabs') && e.getBoundingClientRect().width > 0)
        .map((e) => ({ name: e.getAttribute('aria-label') || e.textContent?.trim() || '', w: e.getBoundingClientRect().width, h: e.getBoundingClientRect().height })),
    };
  }, W);
}

const overlaps = (a: Box, b: Box) => Math.min(a.right, b.right) - Math.max(a.left, b.left) > 1 && Math.min(a.bottom, b.bottom) - Math.max(a.top, b.top) > 1;

async function checkPage(page: Page, where: string) {
  const s = await shell(page);
  expect(s.overflow, `${where}: the page scrolls sideways`).toBeLessThanOrEqual(1);
  expect(s.header, `${where}: no header`).not.toBeNull();
  const header = s.header!;
  // one row everywhere: the phone's top bar, the floating bar on the others
  expect(header.bottom - header.top, `${where}: the header wraps`).toBeLessThanOrEqual(s.layout === 'phone' ? 57 : 57);
  for (const c of s.controls) {
    expect(c.left >= header.left - 1 && c.right <= header.right + 1, `${where}: ${c.name} sticks out of the header`).toBe(true);
  }
  const clashes: string[] = [];
  s.controls.forEach((a, i) => s.controls.slice(i + 1).forEach((b) => overlaps(a, b) && clashes.push(`${a.name} × ${b.name}`)));
  expect(clashes, `${where}: header controls on top of each other`).toEqual([]);
  if (s.tabBar) expect(overlaps(header, s.tabBar)).toBe(false);
  if (s.input === 'touch') {
    const small = s.targets.filter((t) => t.h < 40 || t.w < 40).map((t) => `${t.name} ${Math.round(t.w)}×${Math.round(t.h)}`);
    expect(small, `${where}: too small for a finger`).toEqual([]);
  }
  // the page's title is on screen and not under a bar
  const h1 = page.locator('.page-head h1').first();
  if (await h1.count()) {
    const hidden = await h1.evaluate((el) => {
      const r = el.getBoundingClientRect();
      const hit = document.elementFromPoint(r.left + Math.min(16, r.width / 2), r.top + r.height / 2);
      return !hit || !el.contains(hit);
    });
    expect(hidden, `${where}: the title is covered`).toBe(false);
  }
}

test('every page fits the layout of this screen', async ({ page, hub }, info) => {
  const p = await hub.project('Layout');
  const expected = { phone: 'phone', 'tablet-portrait': 'tablet', 'tablet-landscape': 'tablet' }[info.project.name] ?? 'desktop';
  for (const path of [...PAGES, `/p/${p.id}`]) {
    await page.goto(path);
    await expect(page.locator('.app-header')).toBeVisible();
    await page.waitForTimeout(300);
    expect(await page.evaluate(() => document.documentElement.dataset.layout), path).toBe(expected);
    await checkPage(page, path);
    // a phone has a tab bar: the pages outside a project, Board · Cards · Lead inside one
    await expect(page.locator('.tab-bar')).toHaveCount(expected === 'phone' ? 1 : 0);
  }
});

test('what the header has no room for is in its menu', async ({ page, hub }, info) => {
  test.skip(!['phone', 'tablet-portrait', 'tablet-landscape'].includes(info.project.name), 'the desktop header shows them all');
  const p = await hub.project('More');
  await page.goto(`/p/${p.id}`);
  await page.getByRole('button', { name: 'More', exact: true }).click();
  const menu = page.getByRole('menu', { name: 'More' });
  for (const item of ['Projects', 'Memory', 'Machines', 'Devices', 'Settings', 'About this hub', 'Log out']) await expect(menu.getByRole('menuitem', { name: item })).toBeVisible();
  await menu.getByRole('menuitem', { name: 'About this hub' }).click();
  await expect(page.getByRole('dialog', { name: 'About this hub' })).toBeVisible();
});

test('a phone: one switcher for the project and its workspaces, settings one pane at a time', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'phone', 'phone only');
  const p = await hub.project('Pocket');
  await page.goto(`/p/${p.id}`);
  const switcher = page.getByRole('button', { name: new RegExp(`^Project ${p.name}, workspace Main`) });
  await switcher.click();
  const menu = page.getByRole('menu', { name: 'Switch project' });
  await menu.getByRole('menuitem', { name: 'New workspace' }).click();
  await expect(page).toHaveURL(new RegExp(`/p/${p.id}/w/`));
  await expect(page.getByRole('button', { name: /, workspace (?!Main\.)/ })).toBeVisible();
  // rename it from the same menu: no double-click on a phone
  await page.locator('.phone-switch').click();
  await page.getByRole('menuitem', { name: /^Rename/ }).click();
  const dialog = page.getByRole('dialog', { name: 'Rename workspace' });
  // a dialog on a touch screen does not open the keyboard by itself
  expect(await page.evaluate(() => document.activeElement?.tagName)).not.toBe('INPUT');
  await dialog.getByLabel('Name').fill('Deploy');
  await dialog.getByRole('button', { name: 'Rename' }).click();
  await expect(page.getByRole('button', { name: new RegExp(`workspace Deploy`) })).toBeVisible();

  // Settings: the list, then one pane with a way back
  await page.goto('/settings');
  await expect(page.locator('.settings-nav')).toBeVisible();
  await expect(page.locator('.settings-pane')).toHaveCount(0);
  await page.getByRole('link', { name: /Appearance/ }).click();
  await expect(page.getByRole('heading', { name: 'Appearance' })).toBeVisible();
  await expect(page.locator('.settings-nav')).toBeHidden();
  await page.getByRole('link', { name: 'Settings', exact: true }).first().click();
  await expect(page).toHaveURL(/\/settings$/);
});

test('a phone sheet closes when its head is dragged down', async ({ page }, info) => {
  test.skip(info.project.name !== 'phone', 'phone only');
  await page.goto('/');
  await page.getByRole('button', { name: 'Add project' }).click();
  const dialog = page.getByRole('dialog', { name: 'Add project' });
  await expect(dialog).toBeVisible();
  const head = await dialog.locator('.modal-head').boundingBox();
  const cdp = await page.context().newCDPSession(page);
  const x = head!.x + head!.width / 2;
  let y = head!.y + head!.height / 2;
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchStart', touchPoints: [{ x, y }] });
  for (let i = 0; i < 8; i++) {
    y += 20;
    await cdp.send('Input.dispatchTouchEvent', { type: 'touchMove', touchPoints: [{ x, y }] });
  }
  await cdp.send('Input.dispatchTouchEvent', { type: 'touchEnd', touchPoints: [] });
  await expect(dialog).toBeHidden();
});

test('the layout can be pinned for this browser', async ({ page }, info) => {
  test.skip(info.project.name !== 'tablet-landscape', 'one device is enough');
  await page.goto('/settings/appearance');
  const seg = page.getByRole('radiogroup', { name: 'Layout' });
  await seg.getByRole('radio', { name: 'Desktop' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-layout', 'desktop');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-layout', 'desktop');
  await page.getByRole('radiogroup', { name: 'Layout' }).getByRole('radio', { name: 'Auto' }).click();
  await expect(page.locator('html')).toHaveAttribute('data-layout', 'tablet');
});
