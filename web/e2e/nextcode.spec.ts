import { fileURLToPath } from 'node:url';
import { expect, test } from './hub';

// NextCode (docs/API-agents.md §30): a console with a chat instead of a terminal, on a fake of
// Claude Code headless (fake-nextcode.sh). The board card and a Code view tab of one console
// show the same live chat; a permission and a question are answered there, and the answer stays.

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
    // the answer stays in the chat as the owner's reply, under the question as it was asked
    const asked = tab.locator('.nc-qrow');
    await expect(asked).toContainText('Which colour?');
    await expect(asked).toContainText('Red · Blue');
    const reply = tab.getByRole('group', { name: 'Your answer' });
    await expect(reply).toContainText('Colour');
    await expect(reply).toContainText('Blue');
    const kept = await hub.call('GET', `/api/sessions/${s.id}/nextcode`);
    expect(kept.items.find((i: { kind: string }) => i.kind === 'answer')?.answer?.answers).toEqual({ 'Which colour?': ['Blue'] });
    await page.bringToFront();
    await expect(card.getByText('You chose Blue.')).toBeVisible({ timeout: 15_000 });
    await expect(card.getByRole('group', { name: 'Your answer' })).toContainText('Blue');
    await code.close();
    // and after a reload
    await page.reload();
    await expect(card.getByRole('group', { name: 'Your answer' })).toContainText('Blue', { timeout: 20_000 });

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

test('the owner’s decisions on a plan stay in the chat, under the plan', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  test.setTimeout(90_000);
  await hub.call('PATCH', '/api/settings', { agents: { paths: { local: { claude: FAKE } } } });
  const p = await hub.project('NextCode plans');
  const s = await hub.call('POST', '/api/sessions', { agent: 'nextcode', workspaceId: p.wid });
  try {
    await expect.poll(async () => (await hub.call('GET', `/api/sessions/${s.id}`)).agentState, { timeout: 20_000 }).toBe('ready');
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    const card = page.locator('.react-flow__node', { has: page.locator('.nc-chat') });
    const input = card.getByRole('combobox', { name: `Message to @${s.handle}` });
    await input.fill('plan');
    await input.press('Enter');

    // while it waits, the card holds the plan and its row only its head
    const ask = card.getByRole('group', { name: 'Plan ready' });
    await expect(ask).toContainText('Rate limiting for login', { timeout: 15_000 });
    const rows = card.locator('.nc-planrow');
    await expect(rows.first()).toContainText('needs you');
    await expect(card.locator('.nc-planrow-md')).toHaveCount(0);

    // sent back with a note, then the second plan approved into Accept edits
    await ask.getByRole('button', { name: 'Keep planning…' }).click();
    await ask.getByRole('textbox', { name: 'What to change in the plan' }).fill('Use the Redis we have.');
    await ask.getByRole('button', { name: 'Keep planning', exact: true }).click();
    await expect(ask).toContainText('(v2)', { timeout: 15_000 });
    await ask.getByRole('button', { name: 'Approve, accept edits' }).click();
    await expect(card.getByText('Plan approved. Starting with', { exact: false })).toBeVisible({ timeout: 15_000 });

    // each plan as it was, each decision as the owner's reply under it
    await expect(rows).toHaveCount(2);
    await expect(rows.nth(0).locator('.nc-targ')).toHaveText('Rate limiting for login');
    await expect(rows.nth(1).locator('.nc-targ')).toHaveText('Rate limiting for login (v2)');
    const decisions = card.getByRole('group', { name: 'Your decision' });
    await expect(decisions).toHaveCount(2);
    await expect(decisions.nth(0)).toContainText('Keep planning');
    await expect(decisions.nth(0)).toContainText('“Use the Redis we have.”');
    await expect(decisions.nth(1)).toContainText('Approved');
    await expect(decisions.nth(1)).toContainText('and Accept edits: it edits files without asking');
    // a long plan is folded until Show all
    await rows.nth(1).getByRole('button', { name: 'Show all' }).click();
    await expect(rows.nth(1).locator('.nc-planrow-md.is-all')).toContainText('docs/auth.md');
    await expect(rows.nth(1).getByRole('button', { name: 'Show all' })).toHaveCount(0);

    // the hub keeps the decisions, and the console works in Accept edits from then on
    const kept = await hub.call('GET', `/api/sessions/${s.id}/nextcode`);
    const plans = kept.items.filter((i: { kind: string; answer?: { kind?: string } }) => i.kind === 'answer' && i.answer?.kind === 'plan').map((i: { answer: unknown }) => i.answer);
    expect(plans).toEqual([expect.objectContaining({ message: 'Use the Redis we have.' }), expect.objectContaining({ approved: true, mode: 'acceptEdits' })]);
    expect(plans[0].approved).toBeFalsy();
    expect(kept.status.permissionMode).toBe('acceptEdits');
    await page.reload();
    await expect(card.getByRole('group', { name: 'Your decision' })).toHaveCount(2, { timeout: 20_000 });
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

  // NextCode's own skills and memory, on every account
  await page.getByRole('button', { name: 'Add skill' }).click();
  const dialog = page.getByRole('dialog', { name: 'Add a skill' });
  await dialog.getByLabel('Name').fill('release-check');
  await dialog.getByLabel('When to use it').fill('Before a release');
  await dialog.getByLabel('What to do (Markdown)').fill('1. Run the tests.');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(page.getByRole('list', { name: 'NextCode’s skills' })).toContainText('release-check');
  expect(((await hub.call('GET', '/api/nextcode/skills')).items as { name: string }[]).map((x) => x.name)).toContain('release-check');
  await page.getByRole('button', { name: 'Remove the skill release-check' }).click();
  await expect(page.getByRole('list', { name: 'NextCode’s skills' })).toHaveCount(0);

  await page.getByRole('list', { name: 'NextCode’s memory' }).getByRole('button', { name: /Write|Open/ }).first().click();
  const memory = page.getByRole('dialog', { name: /NextCode’s memory · Every project/ });
  await memory.getByLabel('Notes').fill('- answers in Indonesian');
  await memory.getByRole('button', { name: 'Save' }).click();
  await expect.poll(async () => (await hub.call('GET', '/api/nextcode/memory/global')).text).toBe('- answers in Indonesian\n');
  await hub.call('PUT', '/api/nextcode/memory/global', { text: '' });
});

test('a NextCode console started in the Code view is Ready at once', async ({ page, hub }, info) => {
  test.skip(info.project.name !== 'light' && info.project.name !== 'dark', 'desktop');
  // it is ready before the reply that made it reaches the page: that older reply said
  // "starting" and stayed, since a NextCode console says nothing more until a message
  await hub.call('PATCH', '/api/settings', { agents: { paths: { local: { claude: FAKE } } } });
  const p = await hub.project('NextCode ready');
  try {
    await page.setViewportSize({ width: 1600, height: 1000 });
    await page.goto(`/p/${p.id}/code`);
    await page.getByRole('tab', { name: /^Agents/ }).click();
    await page.getByRole('button', { name: 'New agent' }).click();
    await page.getByRole('menuitem', { name: /^NextCode/ }).first().click();
    const chat = page.locator('.nc-chat').first();
    await expect(chat.locator('.nc-live-text')).toHaveText(/^Ready/, { timeout: 5_000 });
  } finally {
    for (const s of await hub.call<{ id: string; workspaceId: string }[]>('GET', '/api/sessions')) if (s.workspaceId === p.wid) await hub.call('DELETE', `/api/sessions/${s.id}`);
    await hub.call('PATCH', '/api/settings', { agents: { paths: { local: { claude: null } } } });
  }
});
