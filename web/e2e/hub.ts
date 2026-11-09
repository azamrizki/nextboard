import { existsSync, mkdtempSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { expect, test as base, type APIRequestContext } from '@playwright/test';

const headers = { 'X-NextBoard': '1' };
let unique = 0;

/** What a test sets up through the API. Every test makes its own projects. */
export class Hub {
  constructor(private readonly request: APIRequestContext) {}

  async call<T = any>(method: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE', url: string, data?: unknown): Promise<T> {
    const r = await this.request.fetch(url, { method, headers, data });
    expect(r.ok(), `${method} ${url}: ${r.status()} ${await r.text()}`).toBeTruthy();
    return r.json();
  }

  /** All tests share one hub, and the picker lists every project: names are made unique. */
  async project(base: string): Promise<{ id: string; wid: string; name: string; path: string }> {
    const path = mkdtempSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'project-'));
    const name = `${base} ${(unique++).toString(36)}${process.pid.toString(36)}`;
    const p = await this.call('POST', '/api/projects', { machineId: 'local', path, name });
    return { id: p.id, wid: p.workspaces[0].id, name, path };
  }

  memory(pid: string, content: string) {
    return this.call('PUT', `/api/projects/${pid}/memory`, { scope: 'project', content });
  }

  page(owner: string, name: string, title: string, content: string, description = '') {
    return this.call('PUT', `/api/memory/docs/${name}${owner ? `?projectId=${owner}` : ''}`, { title, description, content });
  }

  /** A console of the fake agent, and the id of its node. */
  async console(wid: string): Promise<{ id: string; node: string }> {
    const s = await this.call('POST', '/api/sessions', { agent: 'fakecli', workspaceId: wid });
    const board = await this.call('GET', `/api/workspaces/${wid}/board`);
    return { id: s.id, node: board.nodes.find((n: any) => n.data.sessionId === s.id).id };
  }

  /**
   * How to call the hub's tools as a console of the fake agent, which runs in the project's folder
   * and leaves its token there.
   */
  async tools(sid: string, path: string): Promise<(name: string, args: unknown) => Promise<{ text: string; isError: boolean }>> {
    const file = join(path, '.nb-e2e-console');
    await expect.poll(() => existsSync(file), { timeout: 15_000 }).toBe(true);
    const [token, hook] = readFileSync(file, 'utf8').split('\n');
    const url = hook.replace(/\/nb\/hook$/, '') + '/nb/mcp/' + sid;
    return async (name, args) => {
      const r = await fetch(url, {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
        body: JSON.stringify({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name, arguments: args } }),
      });
      const out = await r.json();
      return { text: out.result.content[0].text, isError: out.result.isError === true };
    };
  }

  ops(wid: string, ops: unknown[]) {
    return this.call('POST', `/api/workspaces/${wid}/board/ops`, { clientId: 'c_e2e', baseRev: 0, ops });
  }

  /** A memory tile with these blocks, wired to these nodes. */
  tile(wid: string, blocks: unknown[], wiredTo: string[] = [], id = 'n_mem0000001') {
    return this.ops(wid, [
      { op: 'put', node: { id, kind: 'memory', x: 40, y: 40, w: 360, h: 380, data: { blocks } } },
      ...wiredTo.map((target, i) => ({ op: 'putEdge', edge: { id: `e_wire00000${i}`, kind: 'wire', source: id, target, data: {} } })),
    ]);
  }

  async blocks(wid: string, id = 'n_mem0000001'): Promise<any[]> {
    const board = await this.call('GET', `/api/workspaces/${wid}/board`);
    return board.nodes.find((n: any) => n.id === id).data.blocks;
  }
}

/** Every test fails on an error in the page or a 5xx from the hub, whatever else it checks. */
export const test = base.extend<{ hub: Hub; problems: string[] }>({
  hub: async ({ request }, use) => use(new Hub(request)),
  problems: [
    async ({ page }, use) => {
      const problems: string[] = [];
      page.on('pageerror', (e) => problems.push(`page error: ${e.message}`));
      page.on('console', (m) => {
        if (m.type() === 'error' && !/favicon|WebSocket|Failed to load resource|noVNC requires a secure context/.test(m.text())) problems.push(`console: ${m.text()}`);
      });
      page.on('response', (r) => {
        if (r.status() >= 500) problems.push(`${r.status()} ${r.url()}`);
      });
      await use(problems);
      expect(problems).toEqual([]);
    },
    { auto: true },
  ],
});

export { expect };
