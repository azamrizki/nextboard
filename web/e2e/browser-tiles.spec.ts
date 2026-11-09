import { expect, test } from './hub';

// Two web tiles with the real browser of This PC. Each gets a browser and a profile of its own,
// since a profile has one running instance and a second tile on it would stay empty. A tile that
// is new asks, because a tab costs nothing and a browser several hundred MB.
test('two web tiles each get a browser of their own', async ({ page, hub }, info) => {
  test.slow();
  const status = await hub.call('GET', '/api/gui/apps');
  test.skip(!status.available, 'no display packages on this machine');
  const apps: { id: string; categories?: string[] }[] = status.apps ?? status.items ?? [];
  test.skip(!apps.some((a) => a.categories?.includes('WebBrowser')), 'no browser on this machine');

  const p = await hub.project(`Browsers ${info.project.name}`);
  const put = (id: string, x: number, url: string) => ({ op: 'put', node: { id, kind: 'web', x, y: 0, w: 640, h: 460, data: { url, realBrowser: true } } });
  await hub.ops(p.wid, [put('n_webtile001', 0, 'https://example.com/'), put('n_webtile002', 700, 'https://example.org/')]);
  const sessions = async () => (await hub.call('GET', '/api/gui/sessions')) as { id: string; name: string; instance?: number; state: string }[];
  try {
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    const tile = (id: string) => page.locator(`.react-flow__node[data-id="${id}"]`);

    await tile('n_webtile001').getByRole('button', { name: 'Open here' }).click();
    await expect.poll(async () => (await sessions()).length, { timeout: 30_000 }).toBe(1);
    await tile('n_webtile002').getByRole('button', { name: 'Open here' }).click();
    await expect.poll(async () => (await sessions()).length, { timeout: 30_000 }).toBe(2);
    await expect.poll(async () => (await sessions()).filter((s) => s.state === 'running').length, { timeout: 60_000 }).toBe(2);
    const list = await sessions();
    expect(list.map((s) => s.instance ?? 1).sort()).toEqual([1, 2]);
    expect(new Set(list.map((s) => s.name)).size).toBe(2);

    // Both tiles show their own: no tile was emptied and no tile was removed.
    await expect(tile('n_webtile001').getByRole('button', { name: 'Open here' })).toHaveCount(0);
    await expect(tile('n_webtile002').getByRole('button', { name: 'Open here' })).toHaveCount(0);
    const board = await hub.call('GET', `/api/workspaces/${p.wid}/board`);
    const nodes = board.nodes as { id: string; data: { sessionId?: string; instance?: number } }[];
    const ids = nodes.filter((n) => n.id.startsWith('n_webtile')).map((n) => n.data.sessionId);
    expect(new Set(ids).size).toBe(2);
    expect(nodes.filter((n) => n.id.startsWith('n_webtile')).map((n) => n.data.instance).sort()).toEqual([1, 2]);

    // A new tile, an address only a browser can open (a private one): it asks, and a tab leaves it
    // nothing to show.
    await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_webtile003', kind: 'web', x: 0, y: 520, w: 640, h: 460, data: {} } }]);
    const third = tile('n_webtile003');
    await third.getByRole('textbox', { name: 'Address' }).fill('http://192.168.77.1/');
    await third.getByRole('textbox', { name: 'Address' }).press('Enter');
    await expect(third.getByTestId('browser-choice')).toContainText('own logins', { timeout: 30_000 });
    expect((await sessions()).length).toBe(2);
    await third.getByRole('button', { name: 'New tab there' }).click();
    await expect(third).toHaveCount(0, { timeout: 30_000 });
    expect((await sessions()).length).toBe(2);

    // Again, and this time a browser here.
    await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_webtile004', kind: 'web', x: 700, y: 520, w: 640, h: 460, data: {} } }]);
    const fourth = tile('n_webtile004');
    await fourth.getByRole('textbox', { name: 'Address' }).fill('http://192.168.77.1/');
    await fourth.getByRole('textbox', { name: 'Address' }).press('Enter');
    await fourth.getByRole('button', { name: 'Browser here' }).click({ timeout: 30_000 });
    await expect.poll(async () => (await sessions()).length, { timeout: 30_000 }).toBe(3);
    await expect(fourth).toHaveCount(1);
  } finally {
    for (const s of await sessions()) await hub.call('DELETE', `/api/gui/sessions/${s.id}`);
    await expect.poll(async () => (await sessions()).length, { timeout: 30_000 }).toBe(0);
  }
});
