import { createHash } from 'node:crypto';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import type { Page } from '@playwright/test';
import { expect, test, type Hub } from './hub';
import { openVault } from './vault';

// A card and an identity filled into a page in a browser tile, only when picked. The page says
// what it wants by autocomplete for some fields and by name or label for others, and hides one
// field: it must get nothing.

const months = Array.from({ length: 12 }, (_, i) => `<option>${String(i + 1).padStart(2, '0')}</option>`).join('');
const years = [2026, 2027, 2028, 2029, 2030].map((y) => `<option>${y}</option>`).join('');
const CHECKOUT = `<!doctype html><title>Checkout</title><form>
<label>Name on card <input id="ccname" autocomplete="cc-name"></label>
<input id="ccnum" name="cardnumber" placeholder="Card number">
<select id="mm" name="exp-month"><option value="">MM</option>${months}</select>
<select id="yy" name="exp-year"><option value="">YYYY</option>${years}</select>
<input id="cvc" name="cvc" placeholder="CVC">
<input id="trap" name="card-number-copy" style="opacity:0">
<h3>Shipping</h3>
<input id="fn" autocomplete="given-name"><input id="ln" name="lastname" placeholder="Last name">
<input id="em" type="email" name="email"><input id="a1" name="address1" placeholder="Street">
<input id="city" name="city"><input id="zip" name="zip">
<select id="country" autocomplete="country"><option value="">-</option><option value="ID">Indonesia</option><option value="US">United States</option></select>
</form>`;

let site: Server;
let port = 0;
test.beforeAll(async () => {
  site = createServer((_req, res) => {
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end(CHECKOUT);
  });
  await new Promise<void>((done) => site.listen(0, '127.0.0.1', done));
  port = (site.address() as AddressInfo).port;
});
test.afterAll(() => site?.close());

async function newItem(page: Page, hub: Hub, type: string, values: Record<string, string>) {
  const before = ((await hub.call('GET', '/api/vault')).items ?? []).length;
  await page.getByRole('button', { name: 'New', exact: true }).click();
  const form = page.locator('#vault-item-form');
  await form.getByRole('radio', { name: type, exact: true }).click();
  for (const [label, v] of Object.entries(values)) await form.getByLabel(label, { exact: true }).fill(v);
  await page.getByRole('button', { name: 'Save' }).click();
  await expect(form).toBeHidden();
  await expect.poll(async () => ((await hub.call('GET', '/api/vault')).items ?? []).length).toBe(before + 1);
}

test('a card and an identity fill a page in a browser tile', async ({ page, hub }) => {
  const p = await hub.project('Fill forms');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_web0000001', kind: 'web', x: 0, y: 0, w: 900, h: 700, data: { url: `http://127.0.0.1:${port}/`, machineId: 'local', port } } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_web0000001`);
  const frame = page.frameLocator('.react-flow__node[data-id="n_web0000001"] iframe');
  await expect(frame.locator('#ccnum')).toBeVisible({ timeout: 20_000 });

  // the items, made in the vault window
  await page.locator('.vault-button').click();
  await openVault(page);
  const tag = p.id.slice(-4);
  await newItem(page, hub, 'Card', { Name: `Visa ${tag}`, 'Cardholder name': 'Ana Putri', Brand: 'Visa', Number: '4111 1111 1111 1111', 'Security code': '123', 'Expiry month': '7', 'Expiry year': '2028' });
  await newItem(page, hub, 'Identity', { Name: `Home ${tag}`, 'First name': 'Ana', 'Last name': 'Putri', Email: 'ana@example.com', Address: 'Jl. Contoh 1', City: 'Malang', 'Postal code': '65111', Country: 'Indonesia' });
  await page.keyboard.press('Escape');

  const tile = page.locator('.react-flow__node[data-id="n_web0000001"]');
  await tile.getByRole('button', { name: 'Fill from Credentials' }).click();
  const dialog = page.getByRole('dialog', { name: /^Fill from Credentials/ });
  await dialog.getByRole('radio', { name: 'Cards' }).click();
  await dialog.getByRole('button', { name: `Fill Visa ${tag}` }).click();
  await expect(dialog).toBeHidden();
  await expect(frame.locator('#ccname')).toHaveValue('Ana Putri');
  await expect(frame.locator('#ccnum')).toHaveValue('4111111111111111');
  await expect(frame.locator('#mm')).toHaveValue('07');
  await expect(frame.locator('#yy')).toHaveValue('2028');
  await expect(frame.locator('#cvc')).toHaveValue('123');
  await expect(frame.locator('#trap')).toHaveValue('');
  // the card's values went nowhere else
  await expect(frame.locator('#fn')).toHaveValue('');

  await tile.getByRole('button', { name: 'Fill from Credentials' }).click();
  await dialog.getByRole('radio', { name: 'Identities' }).click();
  await dialog.getByRole('button', { name: `Fill Home ${tag}` }).click();
  await expect(frame.locator('#fn')).toHaveValue('Ana');
  await expect(frame.locator('#ln')).toHaveValue('Putri');
  await expect(frame.locator('#em')).toHaveValue('ana@example.com');
  await expect(frame.locator('#a1')).toHaveValue('Jl. Contoh 1');
  await expect(frame.locator('#city')).toHaveValue('Malang');
  await expect(frame.locator('#zip')).toHaveValue('65111');
  await expect(frame.locator('#country')).toHaveValue('ID');
  // and left the card alone
  await expect(frame.locator('#ccnum')).toHaveValue('4111111111111111');
});

// A password seen in breaches is flagged when asked, and nothing but a hash prefix leaves the page.
test('passwords are checked against breaches only when asked', async ({ page, hub }) => {
  const p = await hub.project('Leaks');
  const leaked = `hunter2-${p.id}`;
  const fine = `fine-${p.id}`;
  const hash = (s: string) => createHash('sha1').update(s).digest('hex').toUpperCase();
  const sent: string[] = [];
  // the hub relays to the real service; here the page's request is answered on the way
  await page.route('**/api/vault/pwned/*', async (route) => {
    const prefix = route.request().url().split('/').pop()!;
    sent.push(route.request().url());
    const lines = [leaked, fine].filter((s) => hash(s).startsWith(prefix) && s === leaked).map((s) => `${hash(s).slice(5)}:42`);
    await route.fulfill({ status: 200, body: [...lines, '00000000000000000000000000000000000:0'].join('\r\n') });
  });
  await page.goto('/');
  await page.locator('.vault-button').click();
  await openVault(page);
  const tag = p.id.slice(-4);
  await newItem(page, hub, 'Login', { Name: `Old forum ${tag}`, Password: leaked });
  await newItem(page, hub, 'Login', { Name: `Bank ${tag}`, Password: fine });
  expect(sent).toEqual([]);

  const win = page.getByRole('dialog', { name: 'Credentials', exact: true });
  await win.getByRole('button', { name: 'Vault actions' }).click();
  await page.getByRole('menuitem', { name: 'Check passwords for leaks…' }).click();
  const dialog = page.getByRole('dialog', { name: 'Check passwords for leaks' });
  await dialog.getByRole('button', { name: /^Check \d+ password/ }).click();
  const list = dialog.getByRole('list', { name: 'Leaked passwords' });
  await expect(list).toContainText(`Old forum ${tag}`);
  await expect(list).not.toContainText(`Bank ${tag}`);
  for (const url of sent) {
    expect(url).not.toContain(leaked);
    expect(url).not.toContain(hash(leaked).slice(5));
    expect(url.split('/').pop()).toHaveLength(5);
  }
  // Show opens it, and its page flags it too
  await list.getByRole('button', { name: 'Show' }).click();
  await win.getByRole('button', { name: 'Check for leaks' }).click();
  await expect(win.getByRole('status')).toContainText('Seen 42 times in data breaches');
});
