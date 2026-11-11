import { createServer, type Server } from 'node:http';
import { expect, test } from './hub';

// API providers (docs/API-agents.md §10.2): added and tested in Settings against a stand-in API on
// loopback, offered where a console starts, and on the console once it runs. The console is the
// fake agent, so no CLI ever talks to the stand-in, let alone to a real service.

const KEY = 'sk-e2e-SECRET-provider-key-123';

async function fakeApi(): Promise<{ url: string; server: Server }> {
  const server = createServer((req, res) => {
    if (req.headers.authorization !== `Bearer ${KEY}` && req.headers['x-api-key'] !== KEY) {
      res.writeHead(401, { 'content-type': 'application/json' });
      res.end('{"error":{"message":"invalid key"}}');
      return;
    }
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(req.url?.endsWith('/models') ? '{"object":"list","data":[{"id":"glm-4.6"},{"id":"glm-4.5-air"}]}' : '{"id":"m"}');
  });
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', r));
  return { url: `http://127.0.0.1:${(server.address() as { port: number }).port}`, server };
}

test('an API provider is added, tested, and offered where a console starts', async ({ page, hub }) => {
  const api = await fakeApi();
  const made: string[] = [];
  const sessions = async (wid: string) => ((await hub.call('GET', '/api/sessions')) as { id: string; workspaceId: string; providerId?: string }[]).filter((s) => s.workspaceId === wid);
  const p = await hub.project('Provider');
  try {
    await page.goto('/settings/providers');
    await page.getByRole('button', { name: 'Add provider' }).click();
    const d = page.getByRole('dialog', { name: 'Add an API provider' });
    await d.getByLabel('Type').selectOption('openai');
    await d.getByLabel('Name').fill('E2E Router');
    await d.getByLabel('Address').fill(`${api.url}/v1`);
    await d.getByLabel('API', { exact: true }).selectOption('responses');
    // a wrong key is refused, the right one lists the models
    await d.getByLabel('API key').fill('wrong');
    await d.getByRole('button', { name: 'Test the connection' }).click();
    await expect(d.getByText('the key was refused', { exact: false })).toBeVisible();
    await d.getByLabel('API key').fill(KEY);
    await d.getByRole('button', { name: 'Test the connection' }).click();
    await expect(d.getByText('It answers, with 2 models.')).toBeVisible();
    await d.getByLabel('Model', { exact: true }).fill('glm-4.6');
    await d.getByLabel('Model of price 1').fill('glm-4.6');
    await d.getByLabel('input price of glm-4.6').fill('0.6');
    await d.getByLabel('output price of glm-4.6').fill('2.2');
    await d.getByLabel('Monthly budget').fill('5');
    await d.getByRole('button', { name: 'Add provider' }).click();
    await expect(d).toBeHidden();
    const row = page.locator('.provider-row').filter({ hasText: 'E2E Router' });
    await expect(row).toContainText('OpenAI · Responses');
    await expect(row).toContainText('$0.00 of $5.00 this month');
    const list = (await hub.call('GET', '/api/providers')).items as { id: string; name: string; prices?: Record<string, { input: number }> }[];
    const router = list.find((x) => x.name === 'E2E Router')!;
    made.push(router.id);
    expect(router.prices?.['glm-4.6']?.input).toBe(0.6);
    // the key stays on the hub
    expect(await page.content()).not.toContain(KEY);
    expect(JSON.stringify(list)).not.toContain(KEY);

    // offered where a console starts: the fake agent is built on Codex, which runs on the Responses API
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    const canvas = page.locator('.react-flow__pane');
    const box = (await canvas.boundingBox())!;
    await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2, { button: 'right' });
    await page.getByRole('menuitem', { name: /^Spawn agent/ }).click();
    const panel = page.getByRole('dialog', { name: 'Spawn agent' });
    const tile = panel.locator('[role="radio"][title="Fake CLI"]');
    await tile.click();
    await expect(tile).toHaveAttribute('aria-checked', 'true');
    await panel.getByLabel('Login profile').selectOption({ label: 'API · E2E Router' });
    await panel.getByRole('button', { name: /^Spawn / }).click();
    await expect.poll(async () => (await sessions(p.wid)).map((s) => s.providerId ?? '')).toEqual([router.id]);
    await expect(page.locator('.provider-badge')).toBeVisible();
    await expect(page.locator('.provider-badge')).toContainText('API');

    // an Anthropic-compatible one can carry the hidden runs (the lead runs Claude Code)
    const glm = await hub.call('POST', '/api/providers', { name: 'E2E GLM', kind: 'anthropic', baseUrl: api.url, model: 'glm-4.6', apiKey: KEY });
    made.push(glm.id);
    await page.goto('/settings/studios');
    const run = page.getByLabel('Run hidden runs through');
    await expect(run.locator('option', { hasText: 'API · E2E GLM' })).toHaveCount(1);
    await expect(run.locator('option', { hasText: 'API · E2E Router' })).toHaveCount(0);
    await run.selectOption({ label: 'API · E2E GLM' });
    await expect.poll(async () => (await hub.call('GET', '/api/settings')).runner?.provider).toBe(glm.id);

    // the lead: what it thinks with, and what the consoles it starts run on
    await page.goto('/settings/lead');
    const lead = page.getByLabel('Lead runs on');
    await expect(lead.locator('option').first()).toHaveText('Like hidden runs · Claude Code on API · E2E GLM');
    await expect(lead.locator('option', { hasText: 'API · E2E GLM (Claude Code)' })).toHaveCount(1);
    await expect(lead.locator('option', { hasText: 'API · E2E Router (Codex)' })).toHaveCount(1);
    await lead.selectOption({ label: 'Codex · its login' });
    await expect.poll(async () => (await hub.call('GET', '/api/settings')).lead?.runner).toBe('codex');
    await lead.selectOption({ label: 'API · E2E Router (Codex)' });
    await expect.poll(async () => (await hub.call('GET', '/api/settings')).lead?.runner).toBe(router.id);
    const fake = page.getByLabel('Fake CLI consoles the lead starts');
    await expect(fake.locator('option', { hasText: 'API · E2E GLM' })).toHaveCount(0); // Codex-based: no Anthropic API
    await fake.selectOption({ label: 'API · E2E Router' });
    await expect.poll(async () => (await hub.call('GET', '/api/settings')).lead?.providers?.fakecli).toBe(router.id);
    await fake.selectOption({ label: 'Its login' });
    await expect.poll(async () => (await hub.call('GET', '/api/settings')).lead?.providers?.fakecli).toBeUndefined();
    await lead.selectOption({ index: 0 });
    await expect.poll(async () => (await hub.call('GET', '/api/settings')).lead?.runner).toBeUndefined();
  } finally {
    for (const s of await sessions(p.wid)) await hub.call('DELETE', `/api/sessions/${s.id}`);
    for (const id of made) await hub.call('DELETE', `/api/providers/${id}?purge=1`);
    api.server.close();
  }
});
