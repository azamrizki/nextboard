import type { Page } from '@playwright/test';
import { expect } from './hub';

// One vault per hub, shared by every test: made by the first that needs it, opened by the rest.
export const MASTER = 'correct horse battery staple';

/** Creates the vault or unlocks it, in a vault window or card already showing its gate. */
export async function openVault(page: Page) {
  const create = page.getByRole('button', { name: 'Create vault' });
  const unlock = page.getByRole('button', { name: 'Unlock', exact: true });
  await expect(create.or(unlock)).toBeVisible();
  await page.getByLabel('Master password', { exact: true }).fill(MASTER);
  if (await create.isVisible()) {
    await page.getByLabel('Type it again').fill(MASTER);
    await page.getByLabel('I understand it cannot be recovered').check();
    await create.click();
  } else await unlock.click();
  await expect(page.getByRole('button', { name: 'Lock the vault' })).toBeVisible();
}
