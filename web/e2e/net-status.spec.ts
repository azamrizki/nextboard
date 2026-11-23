import { expect, test } from './hub';

// The connection to the hub (docs/API.md, Connection to the hub) and the browser tab (DESIGN.md
// §3.4): signal bars and the round trip in the header, their details with a speed test, the tab
// that says an agent is idle, and a page that a swipe at its top does not reload.

const sheetLayout = (n: string) => n === 'phone' || n === 'tablet-portrait';

test('the header shows the round trip to the hub, and its details run a speed test', async ({ page }, info) => {
  test.setTimeout(90_000);
  const n = info.project.name;
  await page.goto('/');
  const chip = page.locator('.app-header .conn-chip');

  // a ping goes out as soon as the live connection opens
  await expect(chip.locator('.net-bars')).toBeVisible({ timeout: 15_000 });
  await expect(chip).toHaveAccessibleName(/^Live updates connected · \d+ ms round trip to the hub\. Open the connection details$/);
  if (n === 'light' || n === 'dark') await expect(chip.locator('.conn-text')).toHaveText(/^\d+ ms$/);

  await chip.click();
  const details = sheetLayout(n) ? page.getByRole('dialog', { name: 'Connection to the hub' }) : page.locator('.net-popover');
  await expect(details).toBeVisible();
  await expect(details.locator('.net-tile').first()).toContainText(/Latency\s*\d+\s*ms/);
  await expect(details).toContainText(/\d+ reconnects? so far/);
  await expect(details).toContainText(/straight to the hub, live updates over WebSocket/);

  await details.getByRole('button', { name: 'Test speed' }).click();
  await expect(details.getByRole('button', { name: 'Test again' })).toBeVisible({ timeout: 30_000 });
  const tiles = details.locator('.net-tile');
  await expect(tiles.nth(2)).toContainText(/Download\s*[\d.]+\s*Mbps\s*10 MB in/);
  await expect(tiles.nth(3)).toContainText(/Upload\s*[\d.]+\s*Mbps\s*4 MB in/);
  await expect(details.locator('.net-error')).toHaveCount(0);
});

test('Settings › Appearance turns the number off: the chip says Live as before', async ({ page }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  await page.goto('/settings/appearance');
  await page.getByRole('switch', { name: 'Show the round trip in the header' }).click();
  const chip = page.locator('.app-header .conn-chip');
  await expect(chip.locator('.conn-text')).toHaveText('Live');
  await expect(chip.locator('.net-bars')).toHaveCount(0);
  await page.getByRole('switch', { name: 'Show the round trip in the header' }).click();
  await expect(chip.locator('.conn-text')).toHaveText(/^\d+ ms$/, { timeout: 20_000 });
});

test('a swipe at the top of the page does not reload it', async ({ page }) => {
  await page.goto('/');
  for (const el of ['documentElement', 'body'] as const) {
    expect(await page.evaluate((e) => getComputedStyle(document[e]).overscrollBehaviorY, el)).toBe('none');
  }
});
