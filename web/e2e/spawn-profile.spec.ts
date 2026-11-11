import { expect, test } from './hub';

// The ring's Spawn agent picks the login profile too: the default one unless another is chosen,
// and the default again the next time the ring opens.

test('the ring spawns an agent under the login profile picked there', async ({ page, hub }) => {
  const p = await hub.project('Spawn profile');
  const work = await hub.call('POST', '/api/profiles', { machineId: 'local', agent: 'fakecli', name: 'Work', create: true });
  const sessions = async () => ((await hub.call('GET', '/api/sessions')) as { id: string; workspaceId: string; profileId?: string }[]).filter((s) => s.workspaceId === p.wid);
  try {
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    const canvas = page.locator('.react-flow__pane');
    await expect(canvas).toBeVisible();
    const spawnFromRing = async (profile?: string) => {
      const box = (await canvas.boundingBox())!;
      await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2, { button: 'right' });
      await page.getByRole('menuitem', { name: /^Spawn agent/ }).click();
      const panel = page.getByRole('dialog', { name: 'Spawn agent' });
      // tiles show short names ("Fake" twice): pick by the full one, and never spawn before it is
      // picked, since the hub may find a real CLI on this machine
      const tile = panel.locator('[role="radio"][title="Fake CLI"]');
      await tile.click();
      await expect(tile).toHaveAttribute('aria-checked', 'true');
      const select = panel.getByLabel('Login profile');
      await expect(select).toBeVisible();
      // it starts at the default, whatever was picked before
      await expect(select).toHaveValue('');
      if (profile) await select.selectOption({ label: profile });
      await panel.getByRole('button', { name: /^Spawn / }).click();
      await expect(panel).toBeHidden();
    };

    await spawnFromRing('Work');
    await expect.poll(async () => (await sessions()).map((s) => s.profileId ?? '')).toEqual([work.id]);
    // its tile is where the ring opens: clear the board for the next
    for (const s of await sessions()) await hub.call('DELETE', `/api/sessions/${s.id}`);
    await expect(page.locator('.react-flow__node')).toHaveCount(0);

    await spawnFromRing();
    await expect.poll(async () => (await sessions()).length).toBe(1);
    expect((await sessions())[0].profileId ?? '', 'the second console runs under the default profile').not.toBe(work.id);
  } finally {
    for (const s of await sessions()) await hub.call('DELETE', `/api/sessions/${s.id}`);
    await hub.call('DELETE', `/api/profiles/${work.id}?purge=1`);
  }
});
