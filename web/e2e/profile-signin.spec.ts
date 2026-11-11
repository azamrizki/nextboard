import { expect, test } from './hub';

// Adding a login profile on This PC goes on to signing in: the profile's CLI runs in a terminal
// in the dialog, without a tile on any board, and stops when the dialog closes. A profile's row
// can sign in again later.

test('a new profile signs in right in the add dialog', async ({ page, hub }) => {
  const signIns = async () => ((await hub.call('GET', '/api/sessions')) as { id: string; name: string; profileId?: string; panel?: boolean }[]).filter((s) => s.name?.startsWith('Sign in · '));
  let profileId = '';
  try {
    await page.goto('/settings/profiles');
    await page.getByRole('button', { name: 'Add profile' }).click();
    const add = page.getByRole('dialog', { name: 'Add a login profile' });
    await add.getByLabel('Agent').selectOption({ label: 'Fake CLI' });
    await add.getByLabel('Name').fill('Signin test');
    await add.getByRole('button', { name: 'Add profile' }).click();

    const dialog = page.getByRole('dialog', { name: 'Sign in to “Signin test”' });
    await expect(dialog).toBeVisible();
    await expect(dialog.locator('.profile-signin-term .xterm')).toBeVisible({ timeout: 15_000 });
    // the CLI runs under the new profile, with no tile on a board
    await expect.poll(async () => (await signIns()).length).toBe(1);
    const [s] = await signIns();
    profileId = s.profileId ?? '';
    expect(profileId).not.toBe('');
    expect(s.panel).toBe(true);
    await expect(dialog.getByText('“Signin test” is added.', { exact: false })).toBeVisible();

    // closing stops it
    await dialog.getByRole('button', { name: /^(Sign in later|Done)$/ }).click();
    await expect(dialog).toBeHidden();
    await expect.poll(async () => (await signIns()).length).toBe(0);

    // the row signs in again
    await page.getByRole('button', { name: 'Sign in to Signin test' }).click();
    const again = page.getByRole('dialog', { name: 'Sign in to “Signin test”' });
    await expect(again.locator('.profile-signin-term .xterm')).toBeVisible({ timeout: 15_000 });
    await expect.poll(async () => (await signIns()).map((x) => x.profileId)).toEqual([profileId]);
    await again.getByRole('button', { name: /^(Close|Done)$/ }).click();
    await expect.poll(async () => (await signIns()).length).toBe(0);
  } finally {
    for (const s of await signIns()) await hub.call('DELETE', `/api/sessions/${s.id}`);
    if (profileId) await hub.call('DELETE', `/api/profiles/${profileId}?purge=1`);
  }
});
