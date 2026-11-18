import { existsSync } from 'node:fs';
import { join } from 'node:path';
import type { Locator, Page } from '@playwright/test';
import { expect, test } from './hub';
import { quickAction } from './quick';
import { consolesOf, endConsoles, twoParts } from './two-parts';

// "Place in: Root (all parts) / <part>…" (v0.9.0-beta.2, docs/API-board.md §1): every place that
// starts a console in a project of several parts asks where it works, defaulting to the focused
// tile's placement, else the one used last; a project in one folder never asks. Also on the
// owner's phone and tablets: the new console form is a sheet there.

/** The command bar: on a phone it is in the Lead tab (the Board tab is the canvas only). */
async function toBar(page: Page) {
  if (page.viewportSize()!.width >= 600) return;
  await page.locator('.phone-tabs .tab-bar-item').nth(2).click();
  await expect(page.getByRole('region', { name: 'Thread' })).toBeVisible();
}

/** Back to the board's command bar after a start: a phone shows the new console full screen. */
async function backToBar(page: Page, url: string) {
  if (page.viewportSize()!.width >= 600) return;
  await page.goto(url);
  await toBar(page);
}

/** The new console form: a popover from the command bar's agent chip, a sheet on a narrow screen. */
async function newConsoleForm(page: Page) {
  await toBar(page);
  await page.getByRole('button', { name: /^New console settings: / }).click();
  const form = page.getByRole('dialog', { name: /^New (console|terminal)$/ });
  await expect(form).toBeVisible();
  // never a CLI this machine may have: the fake one
  await form.getByLabel('Agent', { exact: true }).selectOption({ label: 'Fake CLI' });
  return form;
}

const start = (form: Locator) => form.getByRole('button', { name: /^(New console|Start terminal)$/ }).click();

test('a new console asks where it works, and remembers it', async ({ page, hub }, info) => {
  const p = await twoParts(hub, 'Shop', { front: 'web' });
  try {
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    await toBar(page);
    const chip = page.getByRole('button', { name: /^New console settings: / });
    // nothing focused, nothing used yet: the root
    await expect(chip).toHaveAccessibleName(/ · Root$/);
    let form = await newConsoleForm(page);
    const place = form.getByRole('radiogroup', { name: 'Place in' });
    await expect(place.getByRole('radio')).toHaveText(['Root', 'admin', 'web', 'Other…']);
    await expect(place.getByRole('radio', { name: 'Root' })).toHaveAttribute('aria-checked', 'true');
    await expect(form).toContainText('On This PC, in a folder with one entry per part: it reaches every part.');
    // the root takes no folder, and the machine follows the placement
    await expect(form.getByLabel('Working directory')).toHaveCount(0);
    await expect(form.getByLabel('Machine', { exact: true })).toHaveCount(0);

    // a part: its folder and machine
    await place.getByRole('radio', { name: 'web' }).click();
    await expect(form).toContainText(`${p.front.path} on This PC`);
    await expect(form.getByLabel('Working directory')).toHaveAttribute('placeholder', p.front.path);
    await start(form);
    await expect(form).toBeHidden();
    await expect.poll(async () => (await consolesOf(hub, p.wid)).map((s) => [s.partId, s.root ?? false, s.cwd])).toEqual([[p.front.id, false, p.front.path]]);
    await expect(page.locator('.react-flow__node .node-part')).toHaveText(['web']);
    // remembered for this project
    await backToBar(page, `/p/${p.id}/w/${p.wid}`);
    await expect(chip).toHaveAccessibleName(/ · web$/);
    if (info.project.name === 'phone') {
      await page.locator('.phone-tabs').getByRole('button', { name: 'Cards' }).click();
      await expect(page.getByRole('button', { name: 'New console · web' })).toBeVisible();
      await toBar(page);
    }

    // and the root again
    form = await newConsoleForm(page);
    await expect(form.getByRole('radiogroup', { name: 'Place in' }).getByRole('radio', { name: 'web' })).toHaveAttribute('aria-checked', 'true');
    await form.getByRole('radiogroup', { name: 'Place in' }).getByRole('radio', { name: 'Root' }).click();
    await start(form);
    await expect(form).toBeHidden();
    await expect.poll(async () => (await consolesOf(hub, p.wid)).filter((s) => s.root).length).toBe(1);
    const root = (await consolesOf(hub, p.wid)).find((s) => s.root)!;
    expect(root.machineId).toBe('local');
    expect(root.cwd).toBe(join(process.env.NEXTBOARD_E2E_ROOT!, 'data', 'roots', p.id));
    await expect(page.locator('.react-flow__node .node-part', { hasText: 'Root' })).toHaveCount(1);
  } finally {
    await endConsoles(hub, p.wid);
  }
});

test('the ring’s Spawn agent asks where the agent works', async ({ page, hub }, info) => {
  test.skip(!!info.project.use.hasTouch, 'the ring opens by right click');
  const p = await twoParts(hub, 'Shop', { front: 'web' });
  try {
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    const canvas = page.locator('.react-flow__pane');
    const box = (await canvas.boundingBox())!;
    await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2, { button: 'right' });
    await page.getByRole('menuitem', { name: /^Spawn agent/ }).click();
    const panel = page.getByRole('dialog', { name: 'Spawn agent' });
    const place = panel.getByRole('radiogroup', { name: 'Place in' });
    await expect(place.getByRole('radio')).toHaveText(['Root', 'admin', 'web']);
    await place.getByRole('radio', { name: 'admin' }).click();
    const tile = panel.locator('[role="radio"][title="Fake CLI"]');
    await tile.click();
    await expect(tile).toHaveAttribute('aria-checked', 'true');
    await panel.getByRole('button', { name: /^Spawn / }).click();
    await expect(panel).toBeHidden();
    await expect.poll(async () => (await consolesOf(hub, p.wid)).map((s) => [s.partId, s.cwd])).toEqual([[p.admin.id, p.admin.path]]);
    await expect(page.locator('.react-flow__node .node-part')).toHaveText(['admin']);
  } finally {
    await endConsoles(hub, p.wid);
  }
});

test('the team composer places every console it starts', async ({ page, hub }) => {
  const p = await twoParts(hub, 'Shop', { front: 'web' });
  try {
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    await toBar(page);
    await quickAction(page, 'Spawn 4');
    let dialog = page.getByRole('dialog', { name: 'Spawn consoles' });
    let place = dialog.getByRole('radiogroup', { name: 'Place in' });
    await expect(place.getByRole('radio')).toHaveText(['Root', 'admin', 'web']);
    await expect(place.getByRole('radio', { name: 'Root' })).toHaveAttribute('aria-checked', 'true');
    await place.getByRole('radio', { name: 'web' }).click();
    await dialog.getByRole('button', { name: 'Every slot runs Fake CLI' }).click();
    await dialog.getByRole('button', { name: 'One console fewer' }).click();
    await dialog.getByRole('button', { name: 'One console fewer' }).click();
    await dialog.getByRole('button', { name: 'Spawn 2' }).click();
    await expect(dialog).toBeHidden({ timeout: 15_000 });
    await expect.poll(async () => (await consolesOf(hub, p.wid)).map((s) => s.partId).sort()).toEqual([p.front.id, p.front.id]);
    await backToBar(page, `/p/${p.id}/w/${p.wid}`);

    // a group at the root: every one of its consoles there
    await quickAction(page, 'Group');
    dialog = page.getByRole('dialog', { name: 'Compose group' });
    place = dialog.getByRole('radiogroup', { name: 'Place in' });
    await expect(place.getByRole('radio', { name: 'web' })).toHaveAttribute('aria-checked', 'true');
    await place.getByRole('radio', { name: 'Root' }).click();
    await dialog.getByRole('button', { name: 'Every slot runs Fake CLI' }).click();
    for (let i = 0; i < 4; i++) await dialog.getByRole('button', { name: 'One console fewer' }).click();
    await dialog.getByRole('button', { name: 'Spawn 2' }).click();
    await expect(dialog).toBeHidden({ timeout: 15_000 });
    await expect.poll(async () => (await consolesOf(hub, p.wid)).filter((s) => s.root).length).toBe(2);
    expect((await consolesOf(hub, p.wid)).length).toBe(4);
  } finally {
    await endConsoles(hub, p.wid);
  }
});

test('a project in one folder never asks where', async ({ page, hub }, info) => {
  const p = await hub.project('Blog');
  try {
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    // the ring's Spawn agent: no "Place in"
    if (!info.project.use.hasTouch) {
      const box = (await page.locator('.react-flow__pane').boundingBox())!;
      await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2, { button: 'right' });
      await page.getByRole('menuitem', { name: /^Spawn agent/ }).click();
      await expect(page.getByRole('dialog', { name: 'Spawn agent' }).locator('[role="radio"][title="Fake CLI"]')).toBeVisible();
      await expect(page.getByRole('dialog', { name: 'Spawn agent' }).getByRole('radiogroup', { name: 'Place in' })).toHaveCount(0);
      await page.keyboard.press('Escape');
      await page.keyboard.press('Escape');
      await expect(page.getByRole('dialog', { name: 'Spawn agent' })).toBeHidden();
    }

    await toBar(page);
    const chip = page.getByRole('button', { name: /^New console settings: / });
    await expect(chip).not.toHaveAccessibleName(/ · /);
    const form = await newConsoleForm(page);
    await expect(form.getByRole('radiogroup', { name: 'Place in' })).toHaveCount(0);
    await expect(form.getByLabel('Machine', { exact: true })).toBeVisible();
    await expect(form.getByLabel('Working directory')).toHaveAttribute('placeholder', p.path);
    await start(form);
    await expect(form).toBeHidden();
    const part = (await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id).parts[0];
    await expect.poll(async () => (await consolesOf(hub, p.wid)).map((s) => [s.partId, s.root ?? false, s.cwd])).toEqual([[part.id, false, p.path]]);
    // no chip on its tile, as before
    await expect(page.locator('.react-flow__node .node-part')).toHaveCount(0);
    await backToBar(page, `/p/${p.id}/w/${p.wid}`);

    // the composer asks nothing either
    await quickAction(page, 'Spawn 4');
    const dialog = page.getByRole('dialog', { name: 'Spawn consoles' });
    await expect(dialog.getByRole('button', { name: /^Spawn \d/ })).toBeVisible();
    await expect(dialog.getByRole('radiogroup', { name: 'Place in' })).toHaveCount(0);
    await page.keyboard.press('Escape');
    await expect(dialog).toBeHidden();

    // a console asked for at the root is in the one part: no root folder is made
    const s = await hub.call('POST', '/api/sessions', { agent: 'fakecli', workspaceId: p.wid, root: true });
    expect(s.partId).toBe(part.id);
    expect(s.root ?? false).toBe(false);
    expect(s.cwd).toBe(p.path);
    expect(existsSync(join(process.env.NEXTBOARD_E2E_ROOT!, 'data', 'roots', p.id))).toBe(false);
    expect((await hub.call('GET', '/api/projects')).find((x: any) => x.id === p.id).rootDir).toBeUndefined();
  } finally {
    await endConsoles(hub, p.wid);
  }
});
