import { expect, test } from './hub';

// This PC's resources (docs/API.md, Host resources): the header pill and its details on a
// desktop and a tablet held sideways, the ring and a sheet on a tablet upright, the "…" menu and
// a sheet on a phone; the Machines page, the Code view's status line and the This PC card.
// The hub reads this machine for real: the numbers are whatever it has.

test("This PC's resources where each layout shows them", async ({ page, hub }, info) => {
  test.setTimeout(90_000);
  const n = info.project.name;
  await page.goto('/');
  const pill = page.locator('.app-header .host-pill');

  if (n === 'phone') {
    // a phone's header has no room: This PC is in its "…"
    await expect(pill).toHaveCount(0);
    await page.getByRole('button', { name: 'More' }).first().click();
    await page.getByRole('menuitem', { name: /^This PC/ }).click();
  } else {
    await expect(pill).toBeVisible({ timeout: 15_000 });
    await expect(pill).toHaveAccessibleName(/^This PC.*CPU \d+%, memory \d+%.*Open This PC$/);
    if (n === 'tablet-portrait') {
      // upright: the ring alone
      await expect(pill.locator('.host-pill-num').first()).toBeHidden();
    } else {
      await expect(pill.locator('.host-pill-num').first()).toHaveText(/^\d+%$/);
    }
    await pill.click();
  }

  const sheet = n === 'phone' || n === 'tablet-portrait';
  const details = sheet ? page.getByRole('dialog', { name: 'This PC' }) : page.locator('.host-popover');
  await expect(details).toBeVisible();
  await expect(details.getByRole('region', { name: 'CPU' })).toContainText(/\d+%/);
  await expect(details.getByRole('region', { name: 'Memory' })).toContainText(/of \d+ GiB/);
  // the hub names itself among the consoles using the most, once it has counted
  await expect(details.locator('.host-console', { hasText: 'NextBoard' })).toBeVisible({ timeout: 15_000 });

  // the details link to the Machines page: its This PC card has gauges, above them the last hour
  await details.getByRole('button', { name: sheet ? 'History' : 'Open in Machines' }).click();
  await expect(page).toHaveURL(/\/machines$/);
  await expect(page.getByRole('region', { name: "This PC's resources" })).toBeVisible();
  await expect(page.getByRole('group', { name: 'This PC now' })).toContainText('CPU');
  await expect(page.locator('.host-history-head')).toContainText('Last hour');
});

test('the Code view has This PC in its status bar, and a This PC card opens as a tab', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  test.setTimeout(90_000);
  const p = await hub.project('This PC');
  const { rev } = await hub.call<{ rev: number }>('GET', `/api/workspaces/${p.wid}/board`);
  await hub.call('POST', `/api/workspaces/${p.wid}/board/ops`, {
    clientId: 'c_e2e',
    baseRev: rev,
    ops: [{ op: 'put', node: { id: 'n_thispc01', kind: 'resources', x: 0, y: 0, w: 520, h: 640, z: 0, data: { title: 'This PC' } } }],
  });

  // the card on the board, live
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const card = page.locator('.react-flow__node-resources');
  await expect(card).toBeVisible({ timeout: 15_000 });
  await expect(card.getByRole('region', { name: 'CPU' })).toContainText(/\d+%/);

  // the Code view: the status line opens the details upward; the card opens as a tab
  await page.setViewportSize({ width: 1600, height: 1000 });
  await page.goto(`/p/${p.id}/code`);
  const status = page.locator('.wb-status .host-status');
  await expect(status).toHaveText(/CPU \d+% · RAM [\d.]+\/\d+ G/, { timeout: 15_000 });
  await status.click();
  await expect(page.locator('.host-popover')).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.locator('.host-popover')).toHaveCount(0);
  await page.getByRole('tab', { name: 'Cards' }).click();
  await page.getByRole('button', { name: 'Open This PC as a tab' }).click();
  await expect(page.locator('.card-tab[data-card="n_thispc01"]').getByRole('region', { name: 'Memory' })).toBeVisible();
});

test("Settings › Notifications has This PC's alert and thresholds", async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light', 'one is enough');
  await page.goto('/settings/notifications');
  await expect(page.getByRole('switch', { name: /This PC runs hot or out of memory/ })).toBeChecked();
  const warm = page.locator('#nt-temp-warn');
  await expect(warm).toHaveValue('80');
  await warm.fill('75');
  await warm.press('Enter');
  await expect.poll(async () => (await hub.call<{ host: { warnTempC: number } }>('GET', '/api/settings')).host.warnTempC).toBe(75);
  await hub.call('PATCH', '/api/settings', { host: { warnTempC: 80 } });
});
