import { createServer, type IncomingMessage, type Server } from 'node:http';
import { expect, test } from './hub';

// NextCode's own engine (docs/API-agents.md §30): no CLI, the hub's own agent on an API provider.
// The provider is a stand-in Chat Completions API on loopback that streams a reply, then a Bash
// call; the agent asks before it runs it, the owner allows it, and the reply after the result
// comes back. Nothing reaches a real service.

const KEY = 'sk-e2e-SECRET-nextcode-api-456';

interface Seen {
  auth: string[];
  bodies: { messages: { role: string; content: string | null }[]; tools?: { function: { name: string } }[] }[];
}

function sse(res: import('node:http').ServerResponse, chunks: object[]) {
  res.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' });
  for (const c of chunks) res.write(`data: ${JSON.stringify(c)}\n\n`);
  res.end('data: [DONE]\n\n');
}

async function body(req: IncomingMessage): Promise<string> {
  let b = '';
  for await (const c of req) b += c;
  return b;
}

async function fakeChat(): Promise<{ url: string; server: Server; seen: Seen }> {
  const seen: Seen = { auth: [], bodies: [] };
  const server = createServer(async (req, res) => {
    seen.auth.push(String(req.headers.authorization ?? ''));
    if (req.method !== 'POST' || !req.url?.endsWith('/chat/completions')) {
      res.writeHead(404, { 'content-type': 'application/json' });
      res.end('{"error":{"message":"not here"}}');
      return;
    }
    const b = JSON.parse(await body(req));
    seen.bodies.push(b);
    const msgs = b.messages as { role: string; content: string | null }[];
    const last = msgs[msgs.length - 1];
    const usage = { choices: [], usage: { prompt_tokens: 120, completion_tokens: 12 } };
    if (last.role === 'tool') {
      sse(res, [{ choices: [{ index: 0, delta: { role: 'assistant', content: `Tests pass: ${String(last.content).trim()}` } }] }, { choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }, usage]);
    } else if (String(last.content).includes('run tests')) {
      sse(res, [
        { choices: [{ index: 0, delta: { role: 'assistant', tool_calls: [{ index: 0, id: 'call_e2e_1', type: 'function', function: { name: 'Bash', arguments: '' } }] } }] },
        { choices: [{ index: 0, delta: { tool_calls: [{ index: 0, function: { arguments: '{"command":"echo ok"}' } }] } }] },
        { choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }] },
        usage,
      ]);
    } else {
      sse(res, [{ choices: [{ index: 0, delta: { role: 'assistant', content: 'Hello from ' } }] }, { choices: [{ index: 0, delta: { content: 'the API engine.' } }] }, { choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }, usage]);
    }
  });
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', r));
  return { url: `http://127.0.0.1:${(server.address() as { port: number }).port}`, server, seen };
}

test('NextCode on its own engine: spawned on an API provider, it chats and asks before a command', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  test.setTimeout(120_000);
  const api = await fakeChat();
  const prov = await hub.call('POST', '/api/providers', { name: 'E2E Chat', kind: 'openai', api: 'chat', baseUrl: `${api.url}/v1`, model: 'e2e-model', apiKey: KEY });
  const p = await hub.project('NextCode API');
  const sessions = async () => ((await hub.call('GET', '/api/sessions')) as { id: string; handle: string; agent: string; workspaceId: string; providerId?: string; agentState?: string }[]).filter((s) => s.workspaceId === p.wid);
  try {
    // Spawn agent: NextCode, the API engine, on the provider (the only one, so it is picked)
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    const canvas = page.locator('.react-flow__pane');
    const box = (await canvas.boundingBox())!;
    await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2, { button: 'right' });
    await page.getByRole('menuitem', { name: /^Spawn agent/ }).click();
    const panel = page.getByRole('dialog', { name: 'Spawn agent' });
    await panel.locator('[role="radio"][title^="NextCode"]').click();
    await panel.getByRole('radio', { name: 'API', exact: true }).click();
    const account = panel.getByLabel('API provider');
    await expect(account).toBeVisible();
    await expect(account.locator('option:checked')).toHaveText(/E2E Chat/);
    await panel.getByRole('button', { name: /^Spawn / }).click();
    await expect.poll(async () => (await sessions()).map((s) => `${s.agent}:${s.providerId ?? ''}`)).toEqual([`nextcode-api:${prov.id}`]);
    const s = (await sessions())[0];
    await expect.poll(async () => (await hub.call('GET', `/api/sessions/${s.id}`)).agentState, { timeout: 20_000 }).toBe('ready');

    // its card is a chat; a message streams the provider's reply
    const card = page.locator('.react-flow__node', { has: page.locator('.nc-chat') });
    await expect(card.locator('.nc-chat.is-card')).toBeVisible({ timeout: 20_000 });
    const input = card.getByRole('combobox', { name: `Message to @${s.handle}` });
    await input.fill('hello');
    await input.press('Enter');
    await expect(card.getByText('Hello from the API engine.')).toBeVisible({ timeout: 20_000 });

    // a command asks first (the default mode); allowed, it runs and its output goes back
    await input.fill('run tests');
    await input.press('Enter');
    const ask = card.locator('.nc-ask');
    await expect(ask).toBeVisible({ timeout: 20_000 });
    await expect(ask).toContainText('echo ok');
    await expect.poll(async () => (await hub.call('GET', `/api/sessions/${s.id}`)).agentState).toBe('needsYou');
    await ask.getByRole('button', { name: 'Allow once' }).click();
    await expect(ask).toHaveCount(0);
    await expect(card.getByText('Tests pass: ok')).toBeVisible({ timeout: 20_000 });
    await expect.poll(async () => (await hub.call('GET', `/api/sessions/${s.id}`)).agentState).toBe('ready');

    // the agent called the provider with its key and the tools; the key never reached the page
    expect(api.seen.auth.every((a) => a === `Bearer ${KEY}`)).toBe(true);
    expect(api.seen.bodies[0].tools?.map((t) => t.function.name)).toEqual(expect.arrayContaining(['Read', 'Edit', 'Bash', 'AskUserQuestion']));
    expect(await page.content()).not.toContain(KEY);
    // what it spent is the provider's (its ledger), in Settings › API providers' figures
    await expect.poll(async () => JSON.stringify(await hub.call('GET', `/api/providers/${prov.id}/spend`))).toContain('e2e-model');
  } finally {
    for (const x of await sessions()) await hub.call('DELETE', `/api/sessions/${x.id}`);
    await hub.call('DELETE', `/api/providers/${prov.id}?purge=1`);
    api.server.close();
  }
});
