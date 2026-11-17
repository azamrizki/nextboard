import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { expect, test, type Hub } from './hub';

// Resume chat (docs/DESIGN.md §3.5 Chats): every conversation an agent had in a project stays in
// the Chats panel after its console is gone and resumes in the folder it was started in; the
// lead's thread is a list of chats. The fake Claude keeps a transcript and logs how it was started.

const read = (dir: string, file: string) => {
  try {
    return readFileSync(join(dir, file), 'utf8');
  } catch {
    return '';
  }
};
const said = (dir: string) => read(dir, '.fake-claude.log');
const starts = (dir: string) => read(dir, '.fake-claude-argv.log');

/** The agent chats' REST is newer than the lead's: a hub without it skips those tests. */
async function hasAgentChats(page: Page, pid: string) {
  const r = await page.request.get(`/api/projects/${pid}/chats`, { headers: { 'X-NextBoard': '1' } });
  return r.ok();
}

/** A fake Claude console that has said one thing, so it has a chat with that title. */
async function chatOf(page: Page, hub: Hub, wid: string, path: string, text: string) {
  const s = await hub.call('POST', '/api/sessions', { agent: 'fakeclaude', workspaceId: wid });
  const info = () => hub.call('GET', `/api/sessions/${s.id}`);
  await expect.poll(async () => (await info()).agentState, { timeout: 20_000 }).toBe('ready');
  const tile = page.locator(`[data-session="${s.id}"]`);
  await tile.locator('.nb-term').click();
  await page.keyboard.type(text);
  await page.keyboard.press('Enter');
  await expect.poll(() => said(path), { timeout: 20_000 }).toContain(text);
  const sid: string = (await info()).agentSessionId;
  expect(sid).toBeTruthy();
  // the hub counts the turn once it has ended (its transcript written)
  const pid = (await info()).projectId;
  await expect
    .poll(async () => ((await hub.call('GET', `/api/projects/${pid}/chats`)).items as any[]).some((c) => c.agentSessionId === sid && c.messages > 0), { timeout: 20_000 })
    .toBe(true);
  return { id: s.id as string, handle: s.handle as string, sid, tile };
}

const openChats = async (page: Page) => {
  await page.getByRole('toolbar', { name: 'Insert' }).getByRole('button', { name: 'Chats' }).click();
  const panel = page.getByRole('complementary', { name: 'Chats' });
  await expect(panel).toBeVisible();
  return panel;
};

test('a chat whose console was deleted resumes in the folder it was started in', async ({ page, hub }) => {
  test.setTimeout(90_000);
  const p = await hub.project('Resume chat');
  test.skip(!(await hasAgentChats(page, p.id)), 'this hub has no agent chats yet');
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const first = await chatOf(page, hub, p.wid, p.path, 'fix the login redirect');

  // the console goes; its chat stays
  await hub.call('DELETE', `/api/sessions/${first.id}`);
  await expect(first.tile).toHaveCount(0);
  const panel = await openChats(page);
  const row = panel.locator('[data-chat]', { hasText: 'fix the login redirect' });
  await expect(row).toContainText('closed');

  // Resume: a dialog that says where, then a new console there
  await row.getByRole('button', { name: 'Resume: fix the login redirect' }).click();
  const dlg = page.getByRole('dialog', { name: 'Resume chat' });
  await expect(dlg).toContainText('The folder it was started in');
  await expect(dlg).toContainText('Done: fix the login redirect');
  await dlg.getByRole('button', { name: 'Resume chat' }).click();
  await expect(dlg).toBeHidden();

  await expect.poll(() => starts(p.path), { timeout: 20_000 }).toContain(`--resume ${first.sid}`);
  const all: any[] = await hub.call('GET', '/api/sessions');
  const again = all.find((s) => s.agentSessionId === first.sid && s.status !== 'exited');
  expect(again?.cwd).toBe(p.path);
  expect(again?.handle).toBe(first.handle);
  await expect(page.locator(`[data-session="${again.id}"]`)).toBeVisible();
  // the row says where it is now
  await expect(row).toContainText(`live in @${first.handle}`);
  await expect(row.getByRole('button', { name: /^Show / })).toBeVisible();
  await hub.call('DELETE', `/api/sessions/${again.id}`);
});

test('a chat whose name another console took resumes under the next free name', async ({ page, hub }) => {
  test.setTimeout(90_000);
  const p = await hub.project('Name taken');
  test.skip(!(await hasAgentChats(page, p.id)), 'this hub has no agent chats yet');
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const old = await chatOf(page, hub, p.wid, p.path, 'write the orders table migration');
  await hub.call('DELETE', `/api/sessions/${old.id}`);

  // another console in the same folder now goes by that name
  const other = await hub.call('POST', '/api/sessions', { agent: 'fakeclaude', workspaceId: p.wid });
  await hub.call('PATCH', `/api/sessions/${other.id}`, { handle: old.handle });
  await expect.poll(async () => (await hub.call('GET', `/api/sessions/${other.id}`)).agentState, { timeout: 20_000 }).toBe('ready');

  const panel = await openChats(page);
  await panel.getByRole('button', { name: 'Resume: write the orders table migration' }).click();
  const dlg = page.getByRole('dialog', { name: 'Resume chat' });
  await expect(dlg.locator('.resume-taken')).toContainText(`@${old.handle} is already on the board`);
  // the next free name, editable; swapping into the idle console that holds the name is offered
  const name = dlg.getByLabel('Name', { exact: true });
  await expect(name).toHaveValue(/^@\S+$/);
  const next = (await name.inputValue()).slice(1);
  expect(next).not.toBe(old.handle);
  await expect(dlg.getByRole('radio', { name: new RegExp(`^Swap into @${old.handle}`) })).toBeEnabled();
  await dlg.getByRole('button', { name: 'Resume chat' }).click();
  await expect(dlg).toBeHidden();

  await expect.poll(() => starts(p.path), { timeout: 20_000 }).toContain(`--resume ${old.sid}`);
  const all: any[] = await hub.call('GET', '/api/sessions');
  const again = all.find((s) => s.agentSessionId === old.sid && s.status !== 'exited');
  expect(again?.handle).toBe(next);
  expect((await hub.call('GET', `/api/sessions/${other.id}`)).handle).toBe(old.handle);
  await hub.call('DELETE', `/api/sessions/${again.id}`);
  await hub.call('DELETE', `/api/sessions/${other.id}`);
});

test('the new console form resumes a chat of the place picked', async ({ page, hub }) => {
  test.setTimeout(90_000);
  const p = await hub.project('NC resume');
  test.skip(!(await hasAgentChats(page, p.id)), 'this hub has no agent chats yet');
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const old = await chatOf(page, hub, p.wid, p.path, 'seeders for the demo shop');
  await hub.call('DELETE', `/api/sessions/${old.id}`);

  await page.getByRole('button', { name: /^New console settings:/ }).click();
  const form = page.getByRole('dialog', { name: 'New console' });
  await form.getByRole('radio', { name: 'Resume a chat' }).click();
  // its transcript opens over the form, and scrolling in it keeps both open
  await form.getByRole('button', { name: 'More for seeders for the demo shop' }).click();
  await page.getByRole('menuitem', { name: 'Transcript' }).click();
  const transcript = page.getByRole('dialog', { name: /^Transcript · / });
  await expect(transcript).toContainText('Done: seeders for the demo shop');
  await transcript.hover();
  await page.mouse.wheel(0, 400);
  await expect(transcript).toBeVisible();
  await expect(form).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(transcript).toBeHidden();
  await form.getByRole('button', { name: 'Resume: seeders for the demo shop' }).click();
  const dlg = page.getByRole('dialog', { name: 'Resume chat' });
  await dlg.getByRole('button', { name: 'Resume chat' }).click();
  await expect.poll(() => starts(p.path), { timeout: 20_000 }).toContain(`--resume ${old.sid}`);
  const all: any[] = await hub.call('GET', '/api/sessions');
  const again = all.find((s) => s.agentSessionId === old.sid && s.status !== 'exited');
  await expect(page.locator(`[data-session="${again.id}"]`)).toBeVisible();
  await hub.call('DELETE', `/api/sessions/${again.id}`);
});

test('an editor card’s agent menu resumes a chat of its place as a pane of that card', async ({ page, hub }) => {
  test.setTimeout(120_000);
  const p = await hub.project('Pane resume');
  test.skip(!(await hasAgentChats(page, p.id)), 'this hub has no agent chats yet');
  const EDITOR = 'n_edit000001';
  writeFileSync(join(p.path, 'README.md'), '# Pane resume\n');
  await hub.ops(p.wid, [{ op: 'put', node: { id: EDITOR, kind: 'editor', x: 0, y: 0, w: 1200, h: 720, data: {} } }]);
  await page.goto(`/p/${p.id}/w/${p.wid}?card=${EDITOR}`);
  await expect(page.locator('.wb-tab', { hasText: 'README.md' })).toBeVisible({ timeout: 15_000 });
  const scrim = page.getByRole('button', { name: 'Close the side bar' });
  if (await scrim.isVisible()) await scrim.dispatchEvent('click');

  // an agent in the editor says something, then is stopped
  await page.getByRole('button', { name: 'Open an agent in a new tab' }).click();
  await page.getByRole('menuitem', { name: 'Fake Claude' }).click();
  const pane = page.locator('.wb-pane-tab');
  await expect(pane.locator('.xterm')).toBeVisible({ timeout: 15_000 });
  const panes = async () => ((await hub.call('GET', '/api/sessions')) as any[]).filter((s) => s.panel && s.workspaceId === p.wid && s.agent === 'fakeclaude' && s.status !== 'exited');
  await expect.poll(async () => (await panes())[0]?.agentState ?? '', { timeout: 20_000 }).toBe('ready');
  const first = (await panes())[0];
  await pane.locator('.xterm').click();
  await page.keyboard.type('explain the readme');
  await page.keyboard.press('Enter');
  await expect.poll(() => said(p.path), { timeout: 20_000 }).toContain('explain the readme');
  const sid: string = (await hub.call('GET', `/api/sessions/${first.id}`)).agentSessionId;
  await expect
    .poll(async () => ((await hub.call('GET', `/api/projects/${p.id}/chats`)).items as any[]).some((c) => c.agentSessionId === sid && c.messages > 0), { timeout: 20_000 })
    .toBe(true);
  await hub.call('DELETE', `/api/sessions/${first.id}`);

  // Resume a chat…: the chats of the card's place; it comes back as a pane of this card
  await page.getByRole('button', { name: 'Open an agent in a new tab' }).click();
  await page.getByRole('menuitem', { name: 'Resume a chat…' }).click();
  const list = page.getByRole('dialog', { name: 'Resume a chat' });
  await list.getByRole('button', { name: 'Resume: explain the readme' }).click();
  const dlg = page.getByRole('dialog', { name: 'Resume chat' });
  await expect(dlg).toContainText('in this editor');
  await dlg.getByRole('button', { name: 'Resume chat' }).click();
  await expect(dlg).toBeHidden();
  await expect.poll(() => starts(p.path), { timeout: 20_000 }).toContain(`--resume ${sid}`);
  const again = (await panes()).find((s) => s.agentSessionId === sid);
  expect(again?.card).toBe(EDITOR);
  await expect(page.locator('.wb-tab', { hasText: `@${again.handle}` })).toBeVisible();
  await hub.call('DELETE', `/api/sessions/${again.id}`);
});

test('the lead keeps its chats: New chat starts fresh, Resume brings an old one back', async ({ page, hub }) => {
  const p = await hub.project('Lead chats');
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  const bar = page.getByRole('combobox', { name: 'Order for your agents' });
  await bar.fill('help');
  await bar.press('Enter');
  const thread = page.getByRole('region', { name: 'Thread' });
  const current = (title: string) => thread.getByRole('button', { name: new RegExp(`^Chat with .+: ${title}\\. Other chats$`) });
  // the first owner message names the chat
  await expect(current('help')).toBeVisible();
  await expect(thread.locator('.msg-user', { hasText: 'help' })).toBeVisible();

  // New chat: the thread is empty, the old chat is kept
  await thread.getByRole('button', { name: 'New chat' }).click();
  await expect(current('New chat')).toBeVisible();
  await expect(thread.locator('.msg-user')).toHaveCount(0);
  await bar.fill('status');
  await bar.press('Enter');
  await expect(current('status')).toBeVisible();
  await expect(thread.locator('.msg-user', { hasText: 'help' })).toHaveCount(0);

  // the list: the current one marked, the other with Resume
  await current('status').click();
  const list = page.getByRole('dialog', { name: /^Chats with / });
  await expect(list.locator('[data-lead-chat]')).toHaveCount(2);
  await expect(list.locator('.lead-chat-row.is-current')).toContainText('status');
  // its size as the hub counts it, kept up with the thread's messages
  const hubCount = async (title: string) => ((await hub.call('GET', `/api/threads/${p.id}/chats`)).items as any[]).find((c) => c.title === title)?.messages;
  await expect.poll(async () => (await list.locator('.lead-chat-row.is-current').textContent())?.includes(`${await hubCount('status')} msgs`)).toBe(true);
  await list.getByRole('button', { name: 'Resume: help' }).click();
  await expect(list).toBeHidden();

  // resumed: its messages, and the divider that says what the lead has of it
  await expect(current('help')).toBeVisible();
  await expect(thread.locator('.msg-user', { hasText: 'help' })).toBeVisible();
  await expect(thread.locator('.msg-user', { hasText: 'status' })).toHaveCount(0);
  await expect(thread.locator('.msg-divider')).toContainText(/^Resumed — /);
  const chats = await hub.call('GET', `/api/threads/${p.id}/chats`);
  expect(chats.items).toHaveLength(2);
  expect(chats.items.find((c: any) => c.id === chats.active)?.title).toBe('help');
});

test('settings say whether a resumed chat is told what changed', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'dark', 'one project at a time: all tests share the hub settings');
  await page.goto('/settings/agents');
  const tell = page.getByRole('switch', { name: 'Tell an agent what changed when its chat resumes' });
  await expect(tell).toBeChecked();
  await tell.click();
  await expect.poll(async () => (await hub.call('GET', '/api/settings')).agents.resumeTellChanges).toBe(false);
  await tell.click();
  await expect.poll(async () => (await hub.call('GET', '/api/settings')).agents.resumeTellChanges).toBe(true);
});
