import { fileURLToPath } from 'node:url';
import { expect, test } from './hub';

// NextCode (docs/API-agents.md §30): a console with a chat instead of a terminal, on a fake of
// Claude Code headless (fake-nextcode.sh). The board card and a Code view tab of one console
// show the same live chat; a permission and a question are answered there.

const FAKE = fileURLToPath(new URL('./fake-nextcode.sh', import.meta.url));

test('a NextCode console is a chat on the board and in the Code view', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  test.setTimeout(120_000);
  // NextCode's CLI is found where Claude Code's is: the fake, for this test only
  await hub.call('PATCH', '/api/settings', { agents: { paths: { local: { claude: FAKE } } } });
  const p = await hub.project('NextCode');
  const s = await hub.call('POST', '/api/sessions', { agent: 'nextcode', workspaceId: p.wid });
  try {
    await expect.poll(async () => (await hub.call('GET', `/api/sessions/${s.id}`)).agentState, { timeout: 20_000 }).toBe('ready');

    // the board: its card is a chat, not a terminal
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    const card = page.locator('.react-flow__node', { has: page.locator('.nc-chat') });
    await expect(card.locator('.nc-chat.is-card')).toBeVisible({ timeout: 20_000 });
    await expect(card.locator('.nb-term, .xterm')).toHaveCount(0);

    const input = card.getByRole('combobox', { name: `Message to @${s.handle}` });
    await input.fill('hello');
    await input.press('Enter');
    await expect(card.locator('.nc-me', { hasText: 'hello' })).toBeVisible();
    await expect(card.getByText('Done: hello')).toBeVisible({ timeout: 15_000 });

    // /effort is NextCode's own, on every engine: the chat says so and the chip follows, no turn
    await input.fill('/effort high');
    await input.press('Enter');
    await expect(card.locator('.nc-notice', { hasText: 'Effort: high, from the next message' })).toBeVisible({ timeout: 15_000 });
    await expect(card.getByRole('button', { name: /^Model: .*high/ })).toBeVisible();
    expect((await hub.call('GET', `/api/sessions/${s.id}`)).effort).toBe('high');

    // a permission request: the card needs you, Allow once runs the tool
    await input.fill('run tests');
    await input.press('Enter');
    const ask = card.locator('.nc-ask');
    await expect(ask).toBeVisible({ timeout: 15_000 });
    await expect(ask).toContainText('npm test');
    await expect.poll(async () => (await hub.call('GET', `/api/sessions/${s.id}`)).agentState).toBe('needsYou');
    await ask.getByRole('button', { name: 'Allow once' }).click();
    await expect(ask).toHaveCount(0);
    await expect(card.getByText('3 tests pass', { exact: false })).toBeVisible({ timeout: 15_000 });
    await expect.poll(async () => (await hub.call('GET', `/api/sessions/${s.id}`)).agentState).toBe('ready');

    // the Code view: the same console as a tab, the same chat, live in both
    const code = await page.context().newPage();
    await code.setViewportSize({ width: 1600, height: 1000 });
    await code.goto(`/p/${p.id}/code`);
    await expect(code.locator('.wb.is-page')).toBeVisible({ timeout: 20_000 });
    const bar = code.locator('.wb-right.is-page');
    if (!(await bar.isVisible())) await code.getByRole('button', { name: /^Toggle the lead and agents side bar/ }).click();
    await bar.getByRole('tab', { name: /^Agents/ }).click();
    await bar.locator('.wb-agent-row', { hasText: `@${s.handle}` }).getByRole('button', { name: `Open @${s.handle} as a tab` }).click();
    const tab = code.locator('.wb-pane-tab .nc-chat.is-tab');
    await expect(tab).toBeVisible({ timeout: 15_000 });
    await expect(tab.getByText('Done: hello')).toBeVisible();
    await expect(tab.locator('.xterm')).toHaveCount(0);

    // a question from the tab, answered there; the board card shows the answer too
    const tabInput = tab.getByRole('combobox', { name: `Message to @${s.handle}` });
    await tabInput.fill('ask');
    await tabInput.press('Enter');
    const q = tab.locator('.nc-ask');
    await expect(q).toContainText('Which colour?', { timeout: 15_000 });
    await q.getByText('Blue', { exact: true }).click();
    await q.getByRole('button', { name: 'Answer', exact: true }).click();
    await expect(tab.getByText('You chose Blue.')).toBeVisible({ timeout: 15_000 });
    await page.bringToFront();
    await expect(card.getByText('You chose Blue.')).toBeVisible({ timeout: 15_000 });
    await code.close();

    // the CLI ran headless with the permission prompt on its pipe, never a terminal
    const fs = await import('node:fs');
    const argv = fs.readFileSync(`${p.path}/.fake-nextcode-argv.log`, 'utf8');
    expect(argv).toContain('--input-format stream-json');
    expect(argv).toContain('--permission-prompt-tool stdio');
  } finally {
    await hub.call('DELETE', `/api/sessions/${s.id}`);
    await hub.call('PATCH', '/api/settings', { agents: { paths: { local: { claude: null } } } });
  }
});

test('Settings › NextCode has its accounts and defaults', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  await page.goto('/settings/nextcode');
  await expect(page.getByText('NextCode is one agent', { exact: false })).toBeVisible({ timeout: 15_000 });
  await expect(page.getByRole('heading', { name: 'Accounts' })).toBeVisible();
  await page.getByRole('radio', { name: 'Accept edits', exact: true }).click();
  await expect.poll(async () => (await hub.call('GET', '/api/settings')).nextcode.permissionMode).toBe('acceptEdits');
  await page.getByRole('radio', { name: 'Ask', exact: true }).click();
  await expect.poll(async () => (await hub.call('GET', '/api/settings')).nextcode.permissionMode).toBe('default');
  // NextCode's effort for new consoles, on every account
  await page.getByRole('radiogroup', { name: 'Effort of new NextCode consoles' }).getByRole('radio', { name: 'xhigh', exact: true }).click();
  await expect.poll(async () => (await hub.call('GET', '/api/settings')).nextcode.effort).toBe('xhigh');
  await page.getByRole('radiogroup', { name: 'Effort of new NextCode consoles' }).getByRole('radio', { name: 'Default', exact: true }).click();
  await expect.poll(async () => (await hub.call('GET', '/api/settings')).nextcode.effort ?? '').toBe('');
});
