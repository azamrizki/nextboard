import { expect, test } from './hub';
import { quickAction } from './quick';
import { SSHD } from './sshd';
import { consolesOf, endConsoles, twoParts } from './two-parts';

// A layout on a project of several parts (v0.9.0-beta.2, docs/API-orchestration.md §13): each
// group works at the root or in a part. Saved from such a board, a layout remembers each lane's
// part by name; deployed on a project without a part of that name, it asks where before it makes
// anything, and the hub refuses such a deploy with 409 place_unmatched.

let sshd: SSHD;
test.beforeAll(async () => {
  test.skip(!SSHD.available(), 'no /usr/sbin/sshd');
  sshd = await SSHD.start();
});
test.afterAll(() => sshd?.stop());

const byPart = async (hub: any, wid: string) => {
  const out: Record<string, number> = {};
  for (const s of await consolesOf(hub, wid)) {
    const k = s.root ? 'root' : (s.partId ?? '');
    out[k] = (out[k] ?? 0) + 1;
  }
  return out;
};

test('a layout deploys its groups in different parts, and a saved one asks for a part this project lacks', async ({ page, hub, request }, info) => {
  test.slow();
  const a = await twoParts(hub, 'Shop', { sshd });
  const b = await twoParts(hub, 'Shop copy', { front: 'web' });
  let saved = '';
  try {
    // Builders in admin, Review on the server's frontend
    await page.goto(`/p/${a.id}/w/${a.wid}`);
    await quickAction(page, 'Layouts');
    let panel = page.getByRole('dialog', { name: 'Layouts' });
    await panel.getByRole('textbox', { name: 'Search layouts' }).fill('Build & review');
    await panel.getByRole('option', { name: /Build & review/ }).click();
    await panel.getByLabel('Fill every lane with').selectOption({ label: 'Fake CLI' });
    const builders = panel.getByLabel('Place Builders in');
    const review = panel.getByLabel('Place Review in');
    // nothing focused or used yet: the root, until picked
    await expect(builders).toHaveValue('@root');
    await expect(review).toHaveValue('@root');
    await builders.selectOption({ label: 'admin' });
    await review.selectOption({ label: 'frontend' });
    // a lane follows its group unless given its own place
    await expect(panel.getByLabel('Place lane r1 in')).toHaveValue('@group');
    await panel.getByRole('button', { name: 'Deploy Build & review' }).click();
    await expect(panel).toHaveCount(0, { timeout: 20_000 });
    await expect.poll(() => byPart(hub, a.wid), { timeout: 20_000 }).toEqual({ [a.admin.id]: 2, [a.front.id]: 1 });

    // saved from this board: each lane remembers its part by name
    const layout = await hub.call('POST', '/api/layouts', { name: `Split crew ${info.project.name}`, workspaceId: a.wid });
    saved = layout.id;
    const parts = Object.fromEntries((layout.lanes as { key: string; group: string; part?: string }[]).map((l) => [l.key, l.part]));
    expect(Object.values(parts).sort()).toEqual(['admin', 'admin', 'frontend']);
    const reviewKey = (layout.groups as { key: string; name: string }[]).find((g) => g.name === 'Review')!.key;

    // another project, with admin but no frontend: the hub will not guess
    const refused = await request.fetch(`/api/workspaces/${b.wid}/deploy`, { method: 'POST', headers: { 'X-NextBoard': '1' }, data: { layoutId: saved, fill: { agent: 'fakecli' } } });
    expect(refused.status()).toBe(409);
    const body = await refused.json();
    expect(body.code).toBe('place_unmatched');
    expect(body.unmatched).toContainEqual(expect.objectContaining({ part: 'frontend' }));
    expect(body.unmatched.map((u: { part: string }) => u.part)).not.toContain('admin');
    expect((await consolesOf(hub, b.wid)).length).toBe(0);

    // the panel asks first: admin matches by name, frontend is to be picked
    await page.goto(`/p/${b.id}/w/${b.wid}`);
    await quickAction(page, 'Layouts');
    panel = page.getByRole('dialog', { name: 'Layouts' });
    await panel.getByRole('textbox', { name: 'Search layouts' }).fill(`Split crew ${info.project.name}`);
    await panel.getByRole('option', { name: new RegExp(`Split crew ${info.project.name}`) }).click();
    const unmatched = panel.getByTestId('layout-unmatched');
    await expect(unmatched).toContainText('Saved in a part this project does not have: “frontend” (Review).');
    await expect(panel.getByLabel('Place Builders in')).toHaveValue(b.admin.id);
    const pick = panel.getByLabel('Place Review in');
    await expect(pick).toHaveValue('');
    await expect(pick).toHaveAttribute('aria-invalid', 'true');
    await expect(pick.locator('option:checked')).toHaveText('Pick (was frontend)');
    await panel.getByLabel('Fill every lane with').selectOption({ label: 'Fake CLI' });
    await panel.getByRole('button', { name: `Deploy Split crew ${info.project.name}` }).click();
    await expect(page.getByText('Pick where Review works first')).toBeVisible();
    expect((await consolesOf(hub, b.wid)).length).toBe(0);
    await pick.selectOption({ label: 'web' });
    await expect(unmatched).toHaveCount(0);
    await panel.getByRole('button', { name: `Deploy Split crew ${info.project.name}` }).click();
    await expect(panel).toHaveCount(0, { timeout: 20_000 });
    await expect.poll(() => byPart(hub, b.wid), { timeout: 20_000 }).toEqual({ [b.admin.id]: 2, [b.front.id]: 1 });
    expect(reviewKey).toBeTruthy();
  } finally {
    await endConsoles(hub, a.wid);
    await endConsoles(hub, b.wid);
    if (saved) await hub.call('DELETE', `/api/layouts/${saved}`);
  }
});

test('a layout deployed at the root puts its consoles there', async ({ page, hub }) => {
  const p = await twoParts(hub, 'Shop', { front: 'web' });
  try {
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    await quickAction(page, 'Layouts');
    const panel = page.getByRole('dialog', { name: 'Layouts' });
    await panel.getByRole('textbox', { name: 'Search layouts' }).fill('Duo');
    await panel.getByRole('option', { name: /^Duo/ }).click();
    await panel.getByLabel('Fill every lane with').selectOption({ label: 'Fake CLI' });
    await expect(panel.getByLabel('Place Duo in')).toHaveValue('@root');
    await panel.getByRole('button', { name: 'Deploy Duo' }).click();
    await expect(panel).toHaveCount(0, { timeout: 20_000 });
    await expect.poll(() => byPart(hub, p.wid), { timeout: 20_000 }).toEqual({ root: 2 });
    await expect(page.locator('.react-flow__node .node-part', { hasText: 'Root' })).toHaveCount(2);
  } finally {
    await endConsoles(hub, p.wid);
  }
});
