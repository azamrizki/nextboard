import { expect, test } from './hub';

// Home's Group arrangement (DESIGN.md §3.11): a sub-group sits on the same grid of cards as its
// parent, and its heading says where it is (Acme / Web / Mobile), then how many projects and
// sub-groups it has. The toggle is still named by the group alone, and still folds it.

test('nested groups: one grid at every depth, each heading saying where it is', async ({ page, hub }) => {
  const t = Math.random().toString(36).slice(2, 7);
  const acme = await hub.call('POST', '/api/project-groups', { name: `Acme ${t}` });
  const web = await hub.call('POST', '/api/project-groups', { name: `Web ${t}`, parentId: acme.id });
  const mobile = await hub.call('POST', '/api/project-groups', { name: `Mobile ${t}`, parentId: web.id });
  for (const [base, group] of [
    ['Shop', acme],
    ['Docs', web],
    ['Site', web],
    ['App', mobile],
  ] as const) {
    const p = await hub.project(`${base} ${t}`);
    await hub.call('PATCH', `/api/projects/${p.id}`, { groupId: group.id });
  }
  try {
    await page.goto('/');
    await page.getByRole('radio', { name: 'Group', exact: true }).click();
    const section = (g: { id: string }) => page.locator(`section[aria-labelledby="grp-${g.id}"]`);
    const heading = (g: { id: string }) => page.locator(`#grp-${g.id}`);

    // where each one is, and what it holds
    await expect(heading(acme)).toContainText('4 projects');
    await expect(heading(acme)).toContainText('1 sub-group');
    await expect(heading(web)).toHaveText(new RegExp(`^Acme ${t}\\s*/\\s*Web ${t}.*3 projects.*1 sub-group`));
    await expect(heading(mobile)).toHaveText(new RegExp(`^Acme ${t}\\s*/\\s*Web ${t}\\s*/\\s*Mobile ${t}.*1 project`));
    await expect(heading(mobile)).not.toContainText('sub-group');
    // the path is for the eye: the toggle and the heading's level say the rest
    const toggle = heading(mobile).getByRole('button', { name: `Mobile ${t}`, exact: true });
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
    await expect(page.getByRole('heading', { level: 4, name: new RegExp(`Mobile ${t}`) })).toBeVisible();

    // one grid: the first card of every depth starts at the same place
    const left = async (g: { id: string }) => (await section(g).locator(':scope > .project-grid .project-card').first().boundingBox())!.x;
    const x = await left(acme);
    expect(Math.abs((await left(web)) - x)).toBeLessThan(1);
    expect(Math.abs((await left(mobile)) - x)).toBeLessThan(1);

    // folding Web folds its sub-group too
    await heading(web).getByRole('button', { name: `Web ${t}`, exact: true }).click();
    await expect(section(mobile)).toHaveCount(0);
    await expect(section(web).locator('.project-card')).toHaveCount(0);
    await heading(web).getByRole('button', { name: `Web ${t}`, exact: true }).click();
    await expect(section(mobile).locator('.project-card')).toHaveCount(1);
  } finally {
    // the projects move up a level, out of this test's groups
    await hub.call('POST', '/api/project-groups/bulk', { ids: [acme.id, web.id, mobile.id], action: 'delete' });
  }
});
