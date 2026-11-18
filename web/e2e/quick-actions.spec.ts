import { expect, test } from './hub';

// Quick actions (DESIGN.md §3.12): one round button left of the command bar; its actions open
// upward as a menu and every way of leaving it closes it.
test('the quick actions open upward from one button and close every way', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop themes; the phone board has no command bar');
  const p = await hub.project(`Quick ${info.project.name}`);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const fab = page.getByRole('button', { name: 'Quick actions' });
  const menu = page.getByRole('menu', { name: 'Quick actions' });
  await expect(fab).toBeVisible({ timeout: 15_000 });
  await expect(fab).toHaveAttribute('aria-haspopup', 'menu');
  await expect(fab).toHaveAttribute('aria-expanded', 'false');
  // no row of chips above the bar any more
  await expect(page.getByRole('toolbar', { name: 'Quick actions' })).toHaveCount(0);

  // open: the first action takes the keyboard, the list grows upward from the button
  await fab.click();
  await expect(fab).toHaveAttribute('aria-expanded', 'true');
  const items = menu.getByRole('menuitem');
  await expect(items).toHaveText(['New console', 'Spawn 4', 'Group', 'Layouts', 'Auto arrange', 'Pause all', 'Resume all', 'Memory']);
  await expect(items.first()).toBeFocused();
  await expect(menu).toHaveCSS('opacity', '1');
  const near = await items.first().boundingBox();
  const far = await items.last().boundingBox();
  const button = await fab.boundingBox();
  expect(near!.y + near!.height).toBeLessThanOrEqual(button!.y);
  expect(far!.y).toBeLessThan(near!.y);
  // and above the input panel, not under it
  const panel = await page.locator('.cmd-panel').boundingBox();
  expect(near!.y + near!.height).toBeLessThanOrEqual(panel!.y);
  await page.keyboard.press('ArrowUp');
  await expect(items.nth(1)).toBeFocused();
  await page.keyboard.press('End');
  await expect(items.last()).toBeFocused();
  await page.keyboard.press('ArrowUp');
  await expect(items.first()).toBeFocused();

  // Esc: closed, the button has the keyboard again
  await page.keyboard.press('Escape');
  await expect(menu).toHaveCount(0);
  await expect(fab).toBeFocused();
  await expect(fab).toHaveAttribute('aria-expanded', 'false');

  // a press elsewhere closes it
  await fab.click();
  await expect(menu).toBeVisible();
  await page.mouse.click(700, 200);
  await expect(menu).toHaveCount(0);

  // the button again closes it
  await fab.click();
  await fab.click();
  await expect(menu).toHaveCount(0);

  // picking an action does it and closes
  await fab.click();
  await menu.getByRole('menuitem', { name: 'Memory' }).click();
  await expect(menu).toHaveCount(0);
  await expect(page.getByRole('complementary', { name: 'Project memory' })).toBeVisible();
});
