import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { expect, test, type Hub } from './hub';

// An agent asks to run a command with a credential, and the owner answers in the browser. One
// vault per hub, shared by every test: made by the first, opened by the rest.
const MASTER = 'correct horse battery staple';

async function openVault(page: Page) {
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

/** A login with a password, shared with the agents of `project` when it is given. */
async function addLogin(page: Page, hub: Hub, name: string, password: string, project?: string): Promise<string> {
  const before = new Set(((await hub.call('GET', '/api/vault')).items ?? []).map((x: { id: string }) => x.id));
  await page.getByRole('button', { name: 'New', exact: true }).click();
  const form = page.locator('#vault-item-form');
  await form.getByLabel('Name', { exact: true }).fill(name);
  await form.getByLabel('Password', { exact: true }).fill(password);
  if (project) {
    await form.getByLabel('Agents may ask to use it').check();
    // Ticked without a project: the form says so instead of saving it.
    await page.getByRole('button', { name: 'Save' }).click();
    await expect(form.getByRole('alert')).toContainText('Pick the projects');
    await form.getByRole('group', { name: 'Projects whose agents may ask' }).getByLabel(project).check();
  }
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(form).toBeHidden();
  let id = '';
  await expect
    .poll(async () => {
      const items: { id: string }[] = (await hub.call('GET', '/api/vault')).items ?? [];
      id = items.find((x) => !before.has(x.id))?.id ?? '';
      return id;
    })
    .not.toBe('');
  return id;
}

const sha = (s: string) => createHash('sha256').update(s).digest('hex');

test('an agent runs a command with a credential it never sees', async ({ page, hub }) => {
  const p = await hub.project('Portal');
  const c = await hub.console(p.wid);
  const call = await hub.tools(c.id, p.path);
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_cred0000001', kind: 'credentials', x: 40, y: 500, w: 960, h: 640, data: { title: 'Credentials' } } }]);
  await page.goto(`/p/${p.id}`);
  await openVault(page);

  const secret = `sk_test_CANARY_${p.name.replace(/\W/g, '')}`;
  const id = await addLogin(page, hub, `Stripe ${p.name}`, secret, p.name);
  // The hub has the name and the names of the values, and nothing else.
  await expect.poll(async () => JSON.stringify((await hub.call('GET', '/api/vault/agents')).items)).toContain(`"id":"${id}"`);
  const entry = ((await hub.call('GET', '/api/vault/agents')).items as any[]).find((e) => e.id === id);
  expect(entry).toMatchObject({ name: `Stripe ${p.name}`, type: 'login', projects: [p.id], values: ['password'] });
  expect(JSON.stringify(entry)).not.toContain('CANARY');

  const list = await call('credential_list', {});
  expect(list.text).toContain(`"Stripe ${p.name}" (login, id ${id}): password`);

  // The call waits for the owner: answered below, it comes back with the output.
  const cmd = `printf 'key=%s\\n' "$STRIPE_KEY"; printf %s "$STRIPE_KEY" | sha256sum | cut -c1-64 > proof.txt`;
  const pending = call('credential_run', { command: cmd, why: 'deploy the test build', secrets: [{ item: id, value: 'password', env: 'STRIPE_KEY' }] });

  const pill = page.locator('.vault-request-pill');
  await expect(pill).toContainText('asks to use a credential');
  await pill.click();
  const dialog = page.getByRole('dialog', { name: /asks to use a credential/ });
  await expect(dialog.locator('.vault-approve-command')).toHaveText(cmd);
  await expect(dialog).toContainText('deploy the test build');
  await expect(dialog.locator('.vault-approve-uses')).toContainText(`Stripe ${p.name} · password as STRIPE_KEY`);
  // Nothing on the page holds the value, the dialog included.
  expect(await page.content()).not.toContain(secret);
  await dialog.getByRole('button', { name: 'Allow once' }).click();
  await expect(dialog).toBeHidden();

  const done = await pending;
  expect(done).toEqual({ text: 'key=••••\n', isError: false });
  expect(readFileSync(join(p.path, 'proof.txt'), 'utf8').trim()).toBe(sha(secret));
  await expect(pill).toBeHidden();
});

test('the owner says no, and an entry the vault does not back opens nothing', async ({ page, hub }) => {
  const p = await hub.project('Portal');
  const c = await hub.console(p.wid);
  const call = await hub.tools(c.id, p.path);
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_cred0000001', kind: 'credentials', x: 40, y: 500, w: 960, h: 640, data: { title: 'Credentials' } } }]);
  await page.goto(`/p/${p.id}`);
  await openVault(page);
  const shared = await addLogin(page, hub, `Deploy ${p.name}`, `deploy-CANARY-${p.id}`, p.name);
  const private_ = await addLogin(page, hub, `Bank ${p.name}`, `bank-CANARY-${p.id}`);

  // Denied: the command never runs, and the agent reads why.
  const denied = call('credential_run', { command: 'touch ran.txt', why: 'clean up', secrets: [{ item: shared, value: 'password', env: 'TOKEN' }] });
  await page.locator('.vault-request-pill').click();
  const dialog = page.getByRole('dialog', { name: /asks to use a credential/ });
  await dialog.getByLabel('Tell the agent why, if you deny it').fill('not today');
  await dialog.getByRole('button', { name: 'Deny' }).click();
  expect(await denied).toEqual({ text: 'The owner denied this request. They said: not today', isError: true });

  // Anything that can write the hub's files can add a catalog entry. The vault says this item is
  // not shared, so the dialog will not hand it out.
  await hub.call('PUT', `/api/vault/agents/${private_}`, { name: 'Looks harmless', type: 'login', projects: [p.id], values: ['password'] });
  const tampered = call('credential_run', { command: 'echo "$K"', why: 'x', secrets: [{ item: private_, value: 'password', env: 'K' }] });
  await page.locator('.vault-request-pill').click();
  await expect(dialog.locator('.vault-approve-uses')).toContainText(`Bank ${p.name}`);
  await expect(dialog.locator('.vault-approve-uses')).toContainText(`Not shared with agents of ${p.name}.`);
  await expect(dialog.getByRole('button', { name: 'Allow once' })).toBeDisabled();
  await dialog.getByRole('button', { name: 'Deny' }).click();
  expect((await tampered).isError).toBe(true);
  expect(await page.content()).not.toContain('CANARY');

  // The next unlock puts the catalog back in line with the vault.
  await page.reload();
  await openVault(page);
  await expect.poll(async () => ((await hub.call('GET', '/api/vault/agents')).items as any[]).some((e) => e.id === private_)).toBe(false);
});
