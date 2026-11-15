import { existsSync, readFileSync, statSync } from 'node:fs';
import { hostname } from 'node:os';
import { join } from 'node:path';
import type { Browser, BrowserContext, Locator, Page } from '@playwright/test';
import { expect, test } from './hub';
import { DEFAULT_PASSWORD, headers, PERSONAL, removeInstances, scratch, serviceCalls, startPersonal, type InstanceHub } from './instances';
import { PORT } from './playwright.config';

// Several instances on one machine (README, docs/API.md Instances, ROADMAP #52). Two hubs on one
// host: the shared e2e hub (the `default` instance) and `personal`, started here in the same
// scratch home. One browser holds both logins; About this hub shows each instance's folders and
// the other instances; New instance… on personal makes a third, `side`, which the fake systemctl
// starts; and side's setup wizard copies from personal. Every hub is ended in afterAll, and side's
// folder and unit are removed, so the next project's run starts from none. One worker, at most
// three hubs at once.

test.describe.configure({ mode: 'serial' });

const DEFAULT_BASE = `http://127.0.0.1:${PORT}`;
const SIDE = 'side';
const SIDE_PASSWORD = 'samping123';

let personal: InstanceHub;
/** What New instance… made, for the wizard test after it. */
let created: { base: string; port: number; setupUrl: string } | undefined;

test.beforeAll(async () => {
  test.setTimeout(60_000);
  await removeInstances(); // a run killed halfway may have left one
  personal = await startPersonal();
});

test.afterAll(async () => {
  await personal?.stop();
  await removeInstances();
});

/** A context of its own: no login from the shared storage state, on any port of the host. */
async function freshContext(browser: Browser, problems: string[]): Promise<{ ctx: BrowserContext; page: Page }> {
  const { colorScheme, viewport } = test.info().project.use;
  const ctx = await browser.newContext({ storageState: undefined, colorScheme, viewport });
  await ctx.clearCookies();
  const page = await ctx.newPage();
  watch(page, problems);
  return { ctx, page };
}

function watch(page: Page, problems: string[]) {
  page.on('pageerror', (e) => problems.push(`page error: ${e.message}`));
  page.on('response', (r) => r.status() >= 500 && problems.push(`${r.status()} ${r.url()}`));
}

async function login(page: Page, base: string, password: string) {
  await page.goto(`${base}/login`);
  await page.getByLabel('Admin password').fill(password);
  await page.getByRole('button', { name: 'Log in' }).click();
  await expect(page).not.toHaveURL(/\/login/);
  await expect(page.locator('.app-header')).toBeVisible();
}

async function openAbout(page: Page, base: string): Promise<{ about: Locator; row: (name: string | RegExp) => Locator }> {
  await page.goto(`${base}/`);
  await page.getByRole('button', { name: /^About this hub: NextBoard/ }).click();
  const about = page.getByRole('dialog', { name: 'About this hub' });
  await expect(about).toBeVisible();
  const row = (name: string | RegExp) =>
    about.locator('dt', { hasText: typeof name === 'string' ? new RegExp(`^${name}$`) : name }).locator('xpath=following-sibling::dd[1]');
  return { about, row };
}

test('two instances on one host keep their own logins in one browser', async ({ browser, problems }) => {
  const { ctx, page } = await freshContext(browser, problems);
  try {
    expect(await ctx.cookies()).toEqual([]);
    await login(page, DEFAULT_BASE, DEFAULT_PASSWORD);
    await login(page, personal.base, personal.password);
    // cookies ignore the port: before instances, this login replaced the default's
    await page.goto(`${DEFAULT_BASE}/`);
    await expect(page.locator('.app-header')).toBeVisible();
    await expect(page).not.toHaveURL(/\/login/);
    const names = (await ctx.cookies()).map((c) => c.name);
    expect(names).toEqual(expect.arrayContaining(['nb_session_http', `nb_session_http_${PERSONAL}`]));
    expect(names.filter((n) => n.startsWith('nb_session'))).toHaveLength(2);

    // logging out of personal leaves the default signed in
    await page.goto(`${personal.base}/`);
    await page.getByRole('button', { name: 'Log out' }).click();
    await expect(page).toHaveURL(/\/login/);
    expect((await ctx.request.get(`${personal.base}/api/auth/me`)).status()).toBe(401);
    expect((await ctx.request.get(`${DEFAULT_BASE}/api/auth/me`)).status()).toBe(200);
    await page.goto(`${DEFAULT_BASE}/`);
    await expect(page.locator('.app-header')).toBeVisible();
    await expect(page).not.toHaveURL(/\/login/);

    // browser storage is per origin: what one instance keeps, the other does not see
    await page.evaluate(() => localStorage.setItem('nb.e2eMark', 'default'));
    await page.goto(`${personal.base}/login`);
    expect(await page.evaluate(() => localStorage.getItem('nb.e2eMark'))).toBeNull();
  } finally {
    await ctx.close();
  }
});

test('each instance names itself: the manifest and the titles', async ({ page, browser, request, problems }) => {
  // two installed apps that do not look the same on a dock or a home screen
  const a = await (await request.get(`${DEFAULT_BASE}/manifest.json`)).json();
  const b = await (await request.get(`${personal.base}/manifest.json`)).json();
  expect(a.name).toBe('NextBoard · devbox');
  expect(b.name).toBe(`NextBoard · ${personal.hubName}`);
  expect(b.short_name).toBe(personal.hubName);
  // the default instance keeps the titles it had; a named one ends in its hub name
  await page.goto(`${DEFAULT_BASE}/settings`);
  await expect(page).toHaveTitle(/— NextBoard$/);
  const { ctx, page: other } = await freshContext(browser, problems);
  try {
    await login(other, personal.base, personal.password);
    await other.goto(`${personal.base}/settings`);
    await expect(other).toHaveTitle(new RegExp(`— ${personal.hubName}$`));
  } finally {
    await ctx.close();
  }
});

test('About this hub shows the instance, its folders and the other instances', async ({ page, browser, problems }) => {
  const { root, home } = scratch();
  // the shared hub: the default instance, started by hand, with personal beside it
  const { about, row } = await openAbout(page, DEFAULT_BASE);
  await expect(row('Instance')).toHaveText(/^default/);
  await expect(row('Data folder')).toContainText(join(root, 'data'));
  await expect(row('Config file')).toContainText(join(root, 'data', 'config.json'));
  await expect(row('Service')).toContainText(/Not a service/);
  // each path row can be copied, also over plain HTTP (where it selects the text)
  await expect(row('Data folder').getByRole('button', { name: /Copy/ })).toBeVisible();
  const link = row(/^Other instances/).getByRole('link', { name: new RegExp(PERSONAL) });
  await expect(link).toHaveAttribute('href', new RegExp(`^${personal.base}`));
  expect((await page.request.get(new URL('/api/health', (await link.getAttribute('href'))!).toString())).ok()).toBe(true);
  // said plainly: instances of one OS user keep data apart, not secrets
  await expect(about).toContainText(/not a confidentiality boundary|can read/i);
  await expect(about.getByRole('button', { name: 'New instance…' })).toBeVisible();

  // personal's own panel names its own folder
  const { ctx, page: other } = await freshContext(browser, problems);
  try {
    await login(other, personal.base, personal.password);
    const mine = await openAbout(other, personal.base);
    await expect(mine.row('Instance')).toHaveText(new RegExp(`^${PERSONAL}`));
    await expect(mine.row('Data folder')).toContainText(join(home, `.nextboard-${PERSONAL}`));
    await expect(mine.row('Config file')).toContainText(join(home, `.nextboard-${PERSONAL}`, 'config.json'));
    await expect(mine.about.getByRole('link', { name: new RegExp(`^${PERSONAL}\\b`) })).toHaveCount(0);
  } finally {
    await ctx.close();
  }
});

test('New instance… from the info panel makes one and gives its setup link', async ({ browser, problems }) => {
  test.setTimeout(90_000);
  const { home } = scratch();
  const { ctx, page } = await freshContext(browser, problems);
  try {
    await login(page, personal.base, personal.password);
    const { about } = await openAbout(page, personal.base);
    await about.getByRole('button', { name: 'New instance…' }).click();
    const dialog = page.getByRole('dialog', { name: 'New instance' });
    await expect(dialog).toBeVisible();

    // the name is checked as it is typed
    const name = dialog.getByLabel('Name', { exact: true });
    await name.fill(PERSONAL);
    await expect(dialog).toContainText(/already/i);
    await name.fill(SIDE);
    await expect(dialog.getByLabel('Hub name')).toHaveValue(`${hostname()} · ${SIDE}`);
    await dialog.getByLabel('Hub name').fill(`devbox · ${SIDE}`);
    // personal listens on loopback, so the new one does too
    await expect(dialog.getByLabel(/This machine only/)).toBeChecked();
    const port = Number(await dialog.getByLabel('Port').inputValue());
    expect(port % 100).toBe(12); // a block of its own: 12312 + 100·k
    await dialog.getByLabel(/Start from this instance’s settings|Start from this instance's settings/).check();

    // a wrong password is refused, and nothing is made
    await dialog.getByLabel(/admin password/i).fill('salah-sekali');
    await dialog.getByRole('button', { name: /^Create/ }).click();
    await expect(dialog.getByRole('alert')).toContainText(/password/i);
    expect(existsSync(join(home, `.nextboard-${SIDE}`))).toBe(false);

    await dialog.getByLabel(/admin password/i).fill(personal.password);
    await dialog.getByRole('button', { name: /^Create/ }).click();
    const open = dialog.getByRole('link', { name: 'Open setup' });
    await expect(open).toBeVisible({ timeout: 30_000 });
    const href = (await open.getAttribute('href'))!;
    expect(href).toMatch(new RegExp(`^http://127\\.0\\.0\\.1:${port}/setup\\?code=[A-Za-z0-9-]+&from=${PERSONAL}$`));
    await expect(open).toHaveAttribute('target', '_blank');
    await expect(open).toHaveAttribute('rel', /noopener/);

    // its own unit, which the (fake) service manager started; its own folder and config
    expect(serviceCalls()).toEqual(expect.arrayContaining([`systemctl --user enable nextboard-${SIDE}`, `systemctl --user restart nextboard-${SIDE}`]));
    const unit = readFileSync(join(home, '.config', 'systemd', 'user', `nextboard-${SIDE}.service`), 'utf8');
    expect(unit).toContain(`--instance ${SIDE}`);
    expect(unit).toContain(join(home, `.nextboard-${SIDE}`));
    expect(statSync(join(home, `.nextboard-${SIDE}`)).mode & 0o777).toBe(0o700);
    expect(statSync(join(home, `.nextboard-${SIDE}`, 'config.json')).mode & 0o777).toBe(0o600);
    const cfg = JSON.parse(readFileSync(join(home, `.nextboard-${SIDE}`, 'config.json'), 'utf8'));
    expect(cfg).toMatchObject({ instance: SIDE, listen: `127.0.0.1:${port}` });
    expect(cfg.hubId).toMatch(/^[a-z0-9]{10}$/);
    const setupStatus = await (await ctx.request.get(`http://127.0.0.1:${port}/api/setup/status`)).json();
    expect(setupStatus).toMatchObject({ needsSetup: true, instance: SIDE, hubId: cfg.hubId });
    expect(JSON.stringify(setupStatus)).not.toContain(home); // no path before a login

    // Open setup is a plain link: the click opens it in a new tab
    const [tab] = await Promise.all([ctx.waitForEvent('page'), open.click()]);
    await tab.waitForLoadState();
    expect(tab.url()).toBe(href);
    created = { base: `http://127.0.0.1:${port}`, port, setupUrl: href };
  } finally {
    await ctx.close();
  }
});

test('the setup wizard of a new instance: claim, sign-in, copy, summary', async ({ browser, problems }) => {
  test.setTimeout(90_000);
  expect(created, 'New instance… made no instance').toBeTruthy();
  const { base, port, setupUrl } = created!;
  const { home } = scratch();
  // something of personal's to copy
  const fromCtx = await browser.newContext({ storageState: undefined });
  await fromCtx.clearCookies();
  expect((await fromCtx.request.post(`${personal.base}/api/auth/login`, { data: { password: personal.password }, headers })).ok()).toBe(true);
  expect((await fromCtx.request.patch(`${personal.base}/api/settings`, { data: { agents: { sleepAfterMin: 45 } }, headers })).ok()).toBe(true);
  await fromCtx.close();

  const { ctx, page } = await freshContext(browser, problems);
  try {
    await page.goto(setupUrl);
    // 1. claim: which instance, its name only (paths wait for a login); the hub name New
    // instance… was given
    await expect(page.locator('.wizard-instance')).toContainText(`Instance ${SIDE}`);
    await expect(page.locator('body')).not.toContainText(home);
    await expect(page.getByLabel('Setup code')).not.toHaveValue('');
    await expect(page.getByLabel('Hub name')).toHaveValue(`devbox · ${SIDE}`);
    await page.getByLabel('Admin password').fill(SIDE_PASSWORD);
    await page.getByLabel('Confirm password').fill(SIDE_PASSWORD);
    await page.getByRole('button', { name: 'Create admin account' }).click();

    // 2. how its agents sign in: the user's own CLI logins, with the warning that they are shared
    const own = page.getByRole('radio', { name: /My own CLI logins/ });
    await expect(own).toBeVisible();
    await expect(page.getByRole('radio', { name: /Separate NextBoard logins/ })).toBeChecked();
    await own.click();
    await expect(own).toBeChecked();
    await expect(page.getByText(/shared with your terminal/i)).toBeVisible();
    await page.getByRole('button', { name: /^(Continue|Next)$/ }).click();

    // 3. copy from personal, preselected by the link's from=, with personal's own password
    await expect(page.getByLabel(/Copy from/)).toHaveValue(PERSONAL);
    await expect(page.getByLabel('Settings', { exact: true })).toBeChecked();
    await expect(page.getByLabel('Vault', { exact: true })).not.toBeChecked();
    const fromPassword = page.getByLabel(new RegExp(`(admin password of ${PERSONAL}|${PERSONAL}.*admin password)`, 'i'));
    await fromPassword.fill('salah-sekali');
    await page.getByRole('button', { name: /^Copy/ }).click();
    await expect(page.getByRole('alert')).toContainText(/password/i);
    await fromPassword.fill(personal.password);
    await page.getByRole('button', { name: /^Copy/ }).click();
    await expect(page.getByRole('status').filter({ hasText: `Copied from ${PERSONAL}` })).toContainText('settings');
    await page.getByRole('button', { name: /^(Continue|Next)$/ }).click();

    // 4. the agent CLIs found on This PC
    await expect(page.getByRole('list', { name: 'Detected agent CLIs' })).toBeVisible();
    await page.getByRole('button', { name: /^(Continue|Next)$/ }).click();

    // 5. the summary
    await expect(page.getByRole('heading', { name: 'This instance is ready' })).toBeVisible();
    const summary = page.locator('dl.wizard-summary');
    const row = (name: string) => summary.locator('dt', { hasText: new RegExp(`^${name}$`) }).locator('xpath=following-sibling::dd[1]');
    await expect(row('Instance')).toHaveText(SIDE);
    await expect(row('Hub name')).toHaveText(`devbox · ${SIDE}`);
    await expect(row('Data folder')).toContainText(join(home, `.nextboard-${SIDE}`));
    await expect(row('Config file')).toContainText(join(home, `.nextboard-${SIDE}`, 'config.json'));
    await expect(row('Service')).toContainText(`nextboard-${SIDE}`);
    await expect(row('Port')).toContainText(String(port));
    await expect(row('Addresses')).toContainText(base);
    await expect(page.getByText(/not a confidentiality boundary|can read/i)).toBeVisible();
    await page.getByRole('button', { name: /Open the board/ }).click();
    await expect(page.locator('.app-header')).toBeVisible();

    // what the wizard did: settings copied, the sign-in choice, and its own preview ports
    const st = await (await ctx.request.get(`${base}/api/settings`)).json();
    expect(st.agents.sleepAfterMin).toBe(45);
    expect(st.agents.profiles?.local?.claude).toBe('');
    expect(st.previews.portRange).toEqual([port + 8, port + 27]);
  } finally {
    await ctx.close();
  }

  // the setup link works once
  const again = await freshContext(browser, problems);
  try {
    await again.page.goto(setupUrl);
    await expect(again.page.getByText(/already (been )?used|already set up/i)).toBeVisible();
  } finally {
    await again.ctx.close();
  }
});
