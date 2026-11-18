import { expect, type Page } from '@playwright/test';

// The board's quick actions (DESIGN.md §3.12): one round Quick actions button left of the command
// bar opens them upward as a menu. A phone's Board tab has no command bar and offers them
// elsewhere: there the first visible button of that name is used, as the specs did before.

/** Opens Quick actions and picks name; a phone's board clicks its own button of that name. */
export async function quickAction(page: Page, name: string, opts: { exact?: boolean } = {}) {
  const exact = opts.exact ?? true;
  const fab = page.getByRole('button', { name: 'Quick actions' });
  const phone = () => page.evaluate(() => document.documentElement.hasAttribute('data-phone-tab')).catch(() => false);
  // whichever this screen has, once the board is drawn
  await expect.poll(async () => (await fab.isVisible()) || (await phone()), { timeout: 20_000 }).toBe(true);
  if (await fab.isVisible()) {
    if ((await fab.getAttribute('aria-expanded')) !== 'true') await fab.click();
    await page.getByRole('menu', { name: 'Quick actions' }).getByRole('menuitem', { name, exact }).click();
    return;
  }
  await page.getByRole('button', { name, exact }).first().click();
}
