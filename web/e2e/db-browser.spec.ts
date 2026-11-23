import { execFileSync } from 'node:child_process';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { Locator, Page } from '@playwright/test';
import { expect, test, type Hub } from './hub';

// The database browser (docs/API-db.md) against real servers: a throwaway PostgreSQL and Redis in
// Docker, never anything else on the machine. scripts/e2e-shards.sh starts them once before its
// shards and names them in NB_E2E_DB (a container started while another shard's page loads fails
// it with ERR_NETWORK_CHANGED); a run of its own starts and removes its own. Each test gets a
// fresh database of its own. A table with filters, an export, an edit committed, a foreign key
// followed; the SQL editor with a dangerous statement rolled back; the DBeaver-like wizard; Redis
// keys and console; the board's card; a phone. Skipped where Docker or the images are missing.
// NB_DB_SHOTS=<dir> keeps screenshots.

const PG_IMAGE = 'postgres:18.6-alpine';
const REDIS_IMAGE = 'redis:8.4.8-alpine';
const NAME = `nb-dbtest-e2e-${process.pid}`;
const RUN_ON = ['light', 'dark', 'phone'];

/** The servers the shard script started: {pg, pgPort, redis, redisPort, pw}. */
const SHARED = process.env.NB_E2E_DB ? (JSON.parse(process.env.NB_E2E_DB) as { pg: string; pgPort: number; redis: string; redisPort: number; pw: string }) : null;
const PG_PW = SHARED?.pw ?? 'e2e-only-pw';
let pgName = `${NAME}-pg`;
let redisName = `${NAME}-rd`;

const docker = (...args: string[]) => execFileSync('docker', args, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();

function dockerReady(): boolean {
  try {
    docker('image', 'inspect', PG_IMAGE);
    docker('image', 'inspect', REDIS_IMAGE);
    return true;
  } catch {
    return false;
  }
}

const portOf = (c: string, p: string) => Number(docker('port', c, p).split('\n')[0].split(':').pop());

async function until(what: string, ok: () => boolean, ms = 60_000) {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    try {
      if (ok()) return;
    } catch {
      /* not yet */
    }
    await new Promise((r) => setTimeout(r, 500));
  }
  throw new Error(`${what} did not start`);
}

let pgPort = 0;
let redisPort = 0;
let started = false;
let ready = false;
const have = dockerReady();

const SHOP = `
  CREATE TABLE customers (id serial PRIMARY KEY, name text NOT NULL, city text, email text);
  CREATE TABLE orders (id serial PRIMARY KEY, customer_id int REFERENCES customers(id), total numeric(12,2) NOT NULL, status text NOT NULL DEFAULT 'new', created_at timestamptz DEFAULT now());
  INSERT INTO customers (name, city, email) SELECT 'Customer ' || g, (ARRAY['Jakarta','Bandung','Surabaya'])[1 + g % 3], 'c' || g || '@example.test' FROM generate_series(1, 40) g;
  INSERT INTO orders (customer_id, total, status) SELECT 1 + g % 40, (g * 12345) % 900000 / 100.0, (ARRAY['new','paid','shipped'])[1 + g % 3] FROM generate_series(1, 250) g;`;

const psql = (db: string, sql: string) => docker('exec', pgName, 'psql', '-h', '127.0.0.1', '-U', 'postgres', '-d', db, '-v', 'ON_ERROR_STOP=1', '-tAc', sql);

/** A database of this test's own: 40 customers, 250 orders (84 paid). */
function freshShop(): string {
  const db = `shop_${Math.random().toString(36).slice(2, 10)}`;
  psql('postgres', `CREATE DATABASE ${db}`);
  psql(db, SHOP);
  return db;
}

test.beforeAll(async ({}, info) => {
  if (!have || !RUN_ON.includes(info.project.name) || ready) return;
  test.setTimeout(120_000);
  if (SHARED) {
    ({ pg: pgName, pgPort, redis: redisName, redisPort } = SHARED);
  } else {
    docker('run', '-d', '--rm', '--name', pgName, '-e', `POSTGRES_PASSWORD=${PG_PW}`, '-p', '127.0.0.1::5432', PG_IMAGE);
    docker('run', '-d', '--rm', '--name', redisName, '-p', '127.0.0.1::6379', REDIS_IMAGE);
    started = true;
    pgPort = portOf(pgName, '5432/tcp');
    redisPort = portOf(redisName, '6379/tcp');
  }
  // over TCP: the image's first, temporary server listens on its socket only
  await until('PostgreSQL', () => psql('postgres', 'SELECT 1') === '1');
  await until('Redis', () => docker('exec', redisName, 'redis-cli', 'ping') === 'PONG');
  // the same keys every time: tests of other projects may read them meanwhile
  for (const cmd of [
    ['SET', 'greeting', 'hello'],
    ['HSET', 'session:a1', 'user', '7', 'role', 'admin'],
    ['SET', 'session:b2', '{"cart":[1,2]}', 'EX', '3600'],
    ['DEL', 'queue:jobs'],
    ['RPUSH', 'queue:jobs', 'a', 'b', 'c'],
  ])
    docker('exec', redisName, 'redis-cli', ...cmd);
  // new containers bring up network interfaces: a page loading meanwhile fails with
  // ERR_NETWORK_CHANGED, so let the browser see them settle first
  if (started) await new Promise((r) => setTimeout(r, 3000));
  ready = true;
});

test.afterAll(() => {
  ready = false;
  if (!started) return;
  // only the two containers this file started, never the shard script's
  try {
    docker('rm', '-f', pgName, redisName);
  } catch {
    /* already gone */
  }
  started = false;
});

test.beforeEach(({}, info) => {
  test.skip(!have, `Docker with ${PG_IMAGE} and ${REDIS_IMAGE} is needed`);
  test.skip(!RUN_ON.includes(info.project.name), 'desktop in both themes, and a phone');
});

async function shot(page: Page, name: string) {
  const dir = process.env.NB_DB_SHOTS;
  if (dir) await page.screenshot({ path: join(dir, `${test.info().project.name}-${name}.png`) });
}

async function pgConnection(hub: Hub, pid: string, name: string, database = freshShop(), env = 'local') {
  return hub.call('POST', `/api/projects/${pid}/dbs`, {
    connection: { name, driver: 'postgres', env, host: '127.0.0.1', port: pgPort, database, user: 'postgres', savePassword: true, tls: 'off', reach: { kind: 'direct' } },
    secrets: { password: PG_PW },
  });
}

async function openDatabases(page: Page, pid: string) {
  await page.goto(`/p/${pid}/code`);
  const views = page.getByRole('navigation', { name: 'Views' });
  const phoneNav = page.getByRole('navigation', { name: 'Code view' });
  await expect(views.or(phoneNav).first()).toBeVisible({ timeout: 20_000 });
  if (await phoneNav.isVisible()) {
    // a phone: from the Files view's ⋯ Tools
    await phoneNav.getByRole('button', { name: 'Files', exact: true }).click();
    await views.getByRole('button', { name: 'Tools', exact: true }).click();
    await page.getByRole('menuitem', { name: 'Databases', exact: true }).click();
  } else {
    await views.getByRole('button', { name: 'Databases', exact: true }).click();
  }
  const ws = page.locator('.tool-db .db-ws');
  await expect(ws).toBeVisible();
  return ws;
}

/** The connections: behind a button when the workspace is narrow. */
async function connections(ws: Locator) {
  if (await ws.evaluate((el) => el.classList.contains('is-narrow'))) await ws.getByRole('button', { name: 'Connections', exact: true }).click();
  return ws.getByRole('complementary', { name: 'Connections' });
}

async function sqlRun(page: Page, sql: string) {
  const ed = page.locator('.db-tab-body:not([hidden]) .db-sql-editor .cm-content');
  await ed.click();
  await page.keyboard.press('ControlOrMeta+a');
  await page.keyboard.press('Delete');
  await page.keyboard.insertText(sql);
  await page.keyboard.press('Escape');
  await page.keyboard.press('ControlOrMeta+Enter');
}

test('a table: rows, a filter, an edit committed, a foreign key followed', async ({ page, hub }, info) => {
  test.skip(info.project.name === 'phone', 'desktop');
  test.setTimeout(90_000);
  const p = await hub.project('DB table');
  await pgConnection(hub, p.id, 'shop-pg');
  const ws = await openDatabases(page, p.id);

  await (await connections(ws)).getByRole('button', { name: /^shop-pg/ }).click();
  const tree = ws.getByRole('tree', { name: 'shop-pg schema' });
  await expect(tree.getByRole('button', { name: /^orders/ })).toBeVisible({ timeout: 20_000 });
  await tree.getByRole('button', { name: /^orders/ }).click();

  const tab = ws.locator('.db-tab-body:not([hidden])');
  const foot = tab.locator('.db-foot');
  await expect(foot).toContainText('1–100 of 250', { timeout: 15_000 });
  await expect(tab.getByRole('grid', { name: 'orders rows' }).locator('.db-grid-th')).toHaveCount(5);

  // status = paid
  await tab.getByRole('button', { name: 'Filter' }).click();
  await tab.getByLabel('Column').selectOption('status');
  await tab.getByLabel('Value').fill('paid');
  await tab.getByRole('button', { name: 'Add', exact: true }).click();
  await expect(foot).toContainText('of 84');
  await shot(page, 'table');

  // the filtered rows as CSV, from the hub
  await tab.getByRole('button', { name: 'Export' }).click();
  const [file] = await Promise.all([page.waitForEvent('download'), tab.getByRole('menuitem', { name: /^CSV/ }).click()]);
  expect(file.suggestedFilename()).toBe('orders.csv');
  const csv = (await readFile((await file.path())!, 'utf8')).trim().split('\n');
  expect(csv[0]).toBe('id,customer_id,total,status,created_at');
  expect(csv).toHaveLength(85);
  expect(csv.slice(1).every((l) => l.split(',')[3] === 'paid')).toBe(true);

  // the first row's status, edited in the grid, reviewed and committed
  const grid = tab.getByRole('grid', { name: 'orders rows' });
  const statusCol = await grid.locator('.db-grid-th').evaluateAll((ths) => ths.findIndex((th) => th.textContent?.startsWith('status')));
  await grid.locator('.db-grid-row').first().locator('.db-grid-td').nth(statusCol).dblclick();
  await page.keyboard.press('ControlOrMeta+a');
  await page.keyboard.insertText('shipped');
  await page.keyboard.press('Enter');
  await expect(foot).toContainText('1 change');
  await foot.getByRole('button', { name: 'Review SQL' }).click();
  const review = page.getByRole('dialog', { name: 'What Commit runs' });
  await expect(review).toContainText(/UPDATE .*orders.* SET .*status.* = 'shipped' WHERE .*id.* = \d+/);
  await review.getByRole('button', { name: 'Commit' }).click();
  await expect(review).toBeHidden();
  await expect(foot).toContainText('of 83');

  // a customer's row, from the order's foreign key
  await grid.locator('.db-fk-btn').first().click();
  await expect(ws.locator('.db-tab.is-on')).toContainText('customers');
  await expect(ws.locator('.db-tab-body:not([hidden]) .db-foot')).toContainText('of 1');
  await expect(ws.locator('.db-tab-body:not([hidden]) .db-fchip')).toContainText(/id\s*=\s*\d+/);
});

test('the SQL editor: results, a chart, and a DELETE without WHERE rolled back', async ({ page, hub }, info) => {
  test.skip(info.project.name === 'phone', 'desktop');
  test.setTimeout(90_000);
  const p = await hub.project('DB sql');
  await pgConnection(hub, p.id, 'shop-sql');
  const ws = await openDatabases(page, p.id);
  const side = await connections(ws);
  await side.getByRole('button', { name: /^shop-sql/ }).click();
  await side.getByRole('button', { name: 'Actions for shop-sql' }).click();
  await page.getByRole('menuitem', { name: 'New SQL editor' }).click();

  await sqlRun(page, 'SELECT status, count(*) AS n FROM orders GROUP BY status ORDER BY status;');
  const body = ws.locator('.db-tab-body:not([hidden])');
  await expect(body.getByRole('tab', { name: /Result 1 · 3 rows/ })).toBeVisible({ timeout: 15_000 });
  await expect(body.getByRole('grid', { name: 'Result' }).locator('.db-grid-row')).toHaveCount(3);
  await body.getByRole('button', { name: 'Chart', exact: true }).click();
  await expect(body.locator('.db-chart-row')).toHaveCount(3);
  await shot(page, 'sql');

  // every row, asked with the connection's name, run in a transaction, rolled back
  await sqlRun(page, 'DELETE FROM orders');
  const ask = page.getByRole('dialog', { name: 'This deletes every row of orders' });
  await expect(ask).toContainText('about 250 rows');
  const run = ask.getByRole('button', { name: 'Run in a transaction' });
  await expect(run).toBeDisabled();
  await ask.getByLabel('Type shop-sql to run it').fill('shop-sql');
  await shot(page, 'danger');
  await run.click();
  await expect(body.locator('.db-txbar')).toContainText('250 row(s) changed inside a transaction');
  await body.locator('.db-txbar').getByRole('button', { name: 'Roll back' }).click();
  await expect(body.locator('.db-txbar')).toBeHidden();
  await sqlRun(page, 'SELECT count(*) AS n FROM orders');
  await body.getByRole('button', { name: 'Table', exact: true }).click();
  await expect(body.getByRole('grid', { name: 'Result' }).locator('.db-grid-td').first()).toHaveText('250');
});

test('a new connection in the DBeaver flow: Production is read-only', async ({ page, hub }, info) => {
  test.skip(info.project.name === 'phone', 'desktop');
  test.setTimeout(90_000);
  const p = await hub.project('DB wizard');
  const shop = freshShop();
  const ws = await openDatabases(page, p.id);
  await ws.getByRole('button', { name: 'New connection' }).first().click();

  const dlg = page.getByRole('dialog', { name: 'Select your database' });
  await dlg.getByRole('option', { name: /PostgreSQL/ }).click();
  await shot(page, 'wizard-select');
  await dlg.getByRole('button', { name: 'Next ›' }).click();

  const main = page.getByRole('dialog', { name: 'Connection settings' });
  await main.getByRole('textbox', { name: 'Host', exact: true }).fill('127.0.0.1');
  await main.getByRole('textbox', { name: 'Port', exact: true }).fill(String(pgPort));
  await main.getByRole('textbox', { name: 'Database', exact: true }).fill(shop);
  await main.getByLabel('Username').fill('postgres');
  await main.getByLabel('Password', { exact: true }).fill(PG_PW);
  await main.getByRole('button', { name: 'Test connection…' }).click();
  await expect(main.locator('.db-test-ok')).toContainText(/Connected · PostgreSQL 18/, { timeout: 20_000 });
  await main.getByRole('tab', { name: 'SSH' }).click();
  await main.getByLabel('Use SSH tunnel').check();
  await expect(main.locator('.db-route')).toContainText('The way:');
  // an SSH server left empty is said in words, before the hub is asked
  await main.getByRole('button', { name: 'Test tunnel configuration' }).click();
  await expect(main.locator('.db-test-err')).toHaveText(/SSH tunnel: fill in the host and user name of the SSH server/);
  await shot(page, 'wizard-ssh');
  await main.getByLabel('Use SSH tunnel').uncheck();
  await main.getByRole('button', { name: 'Next ›' }).click();

  const details = page.getByRole('dialog', { name: 'Connection details' });
  await details.getByLabel('Connection name').fill('wizard-pg');
  await details.getByRole('radio', { name: /Production/ }).click();
  await expect(details.getByLabel('Read-only connection')).toBeChecked();
  await shot(page, 'wizard-details');
  await details.getByRole('button', { name: 'Finish' }).click();
  await expect(details).toBeHidden();

  // the connection is the hub's, and its password never comes back
  const list = await hub.call('GET', `/api/projects/${p.id}/dbs`);
  const c = list.connections.find((x: { name: string }) => x.name === 'wizard-pg');
  expect(c).toMatchObject({ env: 'production', hasPassword: true, security: { readOnly: true } });
  expect(JSON.stringify(list)).not.toContain(PG_PW);

  await (await connections(ws)).getByRole('button', { name: /^wizard-pg/ }).click();
  await ws.getByRole('tree', { name: 'wizard-pg schema' }).getByRole('button', { name: /^orders/ }).click();
  const tab = ws.locator('.db-tab-body:not([hidden])');
  await expect(tab.locator('.db-foot')).toContainText('Read-only connection', { timeout: 15_000 });
  await expect(tab.getByRole('button', { name: 'Row', exact: true })).toHaveCount(0);
});

test('Redis: keys by prefix, a hash, the console', async ({ page, hub }, info) => {
  test.skip(info.project.name === 'phone', 'desktop');
  test.setTimeout(60_000);
  const p = await hub.project('DB redis');
  await hub.call('POST', `/api/projects/${p.id}/dbs`, { connection: { name: 'cache', driver: 'redis', env: 'local', host: '127.0.0.1', port: redisPort, reach: { kind: 'direct' } }, secrets: {} });
  const ws = await openDatabases(page, p.id);
  await (await connections(ws)).getByRole('button', { name: /^cache/ }).click();
  const keys = ws.getByRole('tree', { name: 'Keys' });
  await expect(keys.getByRole('button', { name: /session:\s*2/ })).toBeVisible({ timeout: 15_000 });
  await keys.getByRole('button', { name: /session:/ }).click();
  await keys.getByRole('button', { name: /a1/ }).click();
  const detail = ws.locator('.db-keydetail');
  await expect(detail).toContainText('session:a1');
  await expect(detail.locator('.db-items')).toContainText('admin');

  await ws.getByLabel('Redis command').fill('HGET session:a1 role');
  await ws.getByLabel('Redis command').press('Enter');
  await expect(ws.locator('.db-console-out')).toContainText('admin');
  await shot(page, 'redis');
});

test('a Database card on the board, kept to one connection', async ({ page, hub }, info) => {
  test.skip(info.project.name === 'phone', 'desktop');
  test.setTimeout(60_000);
  const p = await hub.project('DB card');
  const c = await pgConnection(hub, p.id, 'card-pg');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_dbc0000001', kind: 'database', x: 0, y: 0, w: 1180, h: 720, data: { title: 'card-pg', connectionId: c.id } } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_dbc0000001`);
  const card = page.locator('.react-flow__node[data-id="n_dbc0000001"]');
  await expect(card.locator('.tile-database')).toBeVisible();
  await card.getByRole('button', { name: /card-pg/ }).first().click();
  await expect(card.getByRole('tree', { name: 'card-pg schema' }).getByRole('button', { name: /^customers/ })).toBeVisible({ timeout: 20_000 });
  await shot(page, 'card');
});

test('a phone: the connections behind a button, a table in the room left', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'phone', 'a phone');
  test.setTimeout(60_000);
  const p = await hub.project('DB phone');
  await pgConnection(hub, p.id, 'phone-pg');
  const ws = await openDatabases(page, p.id);
  await (await connections(ws)).getByRole('button', { name: /^phone-pg/ }).click();
  await ws.getByRole('tree', { name: 'phone-pg schema' }).getByRole('button', { name: /^orders/ }).click();
  await expect(ws.locator('.db-tab-body:not([hidden]) .db-foot')).toContainText('of 250', { timeout: 15_000 });
  const vw = page.viewportSize()!.width;
  const b = (await ws.boundingBox())!;
  expect(b.x + b.width).toBeLessThanOrEqual(vw + 1);
  await shot(page, 'phone');
});
