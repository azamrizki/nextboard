import { expect, test, type Hub } from './hub';

// A mission (orchestrated teamwork): the owner approves the lead's plan once, then the hub sends
// each task as an order to its console and the thread pins the mission's card, with Pause,
// Resume and Stop. The consoles are fake agents: a Fake CLI that never takes a turn (and leaves
// its token for the team tools), a Fake Worker that takes turns.

type Console = { id: string; node: string; handle: string };

const handleOf = async (hub: Hub, id: string): Promise<string> => (await hub.call('GET', `/api/sessions/${id}`)).handle;

/** A Fake CLI console, and how to call the team tools as it. */
async function cliConsole(hub: Hub, wid: string, path: string) {
  const c = await hub.console(wid);
  const call = await hub.tools(c.id, path);
  return { c: { ...c, handle: await handleOf(hub, c.id) } as Console, call };
}

/** A Fake Worker console at its prompt. */
async function workerConsole(hub: Hub, wid: string): Promise<Console> {
  const s = await hub.call('POST', '/api/sessions', { agent: 'fakeworker', workspaceId: wid });
  await expect.poll(async () => (await hub.call('GET', `/api/sessions/${s.id}`)).agentState, { timeout: 20_000 }).toBe('ready');
  const board = await hub.call('GET', `/api/workspaces/${wid}/board`);
  return { id: s.id, node: board.nodes.find((n: any) => n.data.sessionId === s.id).id, handle: s.handle };
}

// The planner stays off for the whole run (global-setup.ts): a wake writes its note and no CLI runs.

/** The orders a console got (the hub types them in). */
const ordersTo = async (hub: Hub, sid: string): Promise<string[]> =>
  ((await hub.call('GET', `/api/sessions/${sid}/deliveries`)).items ?? []).filter((d: any) => d.source === 'order').map((d: any) => d.text as string);

const plan = (api: string, web: string) => ({
  goal: 'Build the sign-up form and the endpoint it posts to',
  summary: 'The api agent writes POST /signup; the web agent builds the form against the brief.',
  doneWhen: 'The form posts to the endpoint and both sides have a test.',
  doc: '# Sign-up contract\n\n`POST /signup {email}` answers **201** with the new id.',
  tasks: [
    { id: 't1', handle: api, title: 'Write the sign-up endpoint', text: 'Write POST /signup as in the brief, with a test.' },
    { id: 't2', handle: web, title: 'Build the sign-up form', text: 'Build the sign-up form that posts to /signup, with a test.' },
  ],
});

test('a mission is approved once, sends its orders, and pauses, resumes and stops from its card', async ({ page, hub }) => {
  test.setTimeout(120_000);
  const p = await hub.project('Mission');
  const { c: api, call: asApi } = await cliConsole(hub, p.wid, p.path);
  const web = await workerConsole(hub, p.wid);
  try {
    const m = await hub.call('POST', `/api/projects/${p.id}/missions`, { threadId: p.id, ...plan(api.handle, web.handle) });
    expect(m.state).toBe('awaiting_approval');

    await page.goto(`/p/${p.id}/w/${p.wid}`);
    await page.getByRole('button', { name: /^Open the thread/ }).click();
    const thread = page.getByRole('region', { name: 'Thread' });
    const pinned = thread.locator('.mission-card.is-pinned');
    await expect(pinned).toBeVisible({ timeout: 15_000 });
    await expect(pinned.locator('.mission-state')).toHaveText('Awaiting approval');
    await expect(pinned).toContainText('Build the sign-up form and the endpoint it posts to');

    // the approval: the plan per agent and the brief, no countdown
    const approval = thread.locator('.confirm-card.kind-mission');
    await expect(approval).toBeVisible();
    await expect(approval.getByRole('list', { name: 'Tasks per agent' })).toContainText(`@${api.handle}`);
    await expect(approval).toContainText('Write the sign-up endpoint');
    await expect(approval).toContainText('Build the sign-up form');
    await expect(approval.locator('.confirm-timer')).toHaveCount(0);
    await approval.getByRole('button', { name: 'Shared brief' }).click();
    await expect(approval.locator('.mission-doc')).toContainText('Sign-up contract');
    await approval.locator('.confirm-actions .btn-primary').click();
    await expect(approval.locator('.confirm-state')).toContainText('Approved');

    // running: each task went out as an order to its console
    await expect(pinned.locator('.mission-state')).toHaveText('Running', { timeout: 15_000 });
    await expect.poll(async () => (await ordersTo(hub, api.id)).some((t) => t.includes('Write POST /signup')), { timeout: 30_000 }).toBe(true);
    await expect.poll(async () => (await ordersTo(hub, web.id)).some((t) => t.includes('Build the sign-up form')), { timeout: 30_000 }).toBe(true);
    await expect(pinned.locator('.mission-meta')).toContainText('Round');
    // the time left counts from the approval, not from when the card first showed
    await expect(pinned.locator('.mission-meta')).toContainText('4h left');
    await pinned.getByRole('button', { name: 'Show the mission details' }).click();
    const tasks = pinned.getByRole('list', { name: 'Tasks per agent' });
    await expect(tasks).toContainText(`@${web.handle}`);
    await expect(tasks.locator('.mission-task')).toHaveCount(2);
    await expect(tasks.locator('.mission-task-state').first()).not.toHaveText('Waiting', { timeout: 15_000 });

    // a teammate's question goes through the hub, and the owner sees who asked whom
    const asked = await asApi('team_ask', { to: `@${web.handle}`, question: 'Which field names does the form send?' });
    expect(asked.isError, asked.text).toBe(false);
    const row = thread.locator('.msg-team').filter({ hasText: 'Which field names does the form send?' });
    await expect(row).toBeVisible({ timeout: 15_000 });
    await expect(row.locator('.team-route')).toHaveText(new RegExp(`@${api.handle}.*@${web.handle}`));
    await pinned.getByRole('button', { name: /^Team conversation/ }).click();
    await expect(pinned.locator('.mission-team .msg-team')).toHaveCount(1);
    await expect(pinned.locator('.mission-meta')).toContainText('1 open question');

    // pause and resume
    await pinned.getByRole('button', { name: 'Pause mission' }).click();
    await expect(pinned.locator('.mission-state')).toHaveText('Paused');
    await expect.poll(async () => (await hub.call('GET', `/api/missions/${m.id}`)).state).toBe('paused');
    await pinned.getByRole('button', { name: 'Resume mission' }).click();
    await expect(pinned.locator('.mission-state')).not.toHaveText('Paused');
    await expect.poll(async () => (await hub.call('GET', `/api/missions/${m.id}`)).state).not.toBe('paused');

    // stop, after a confirm: the pinned card goes, the mission stays in the history
    await pinned.getByRole('button', { name: 'Stop mission' }).click();
    const dialog = page.getByRole('dialog', { name: 'Stop the mission?' });
    await dialog.getByRole('button', { name: 'Stop mission' }).click();
    await expect(pinned).toHaveCount(0);
    await expect.poll(async () => (await hub.call('GET', `/api/missions/${m.id}`)).state).toBe('stopped');
    await expect(thread.locator('.msg-mission .mission-card .mission-state').first()).toHaveText('Stopped');
  } finally {
    for (const c of [api, web]) await hub.call('DELETE', `/api/sessions/${c.id}`);
  }
});

test('an orchestrator card shows its mission in its header and approves it from the pinned card', async ({ page, hub }) => {
  test.setTimeout(90_000);
  const p = await hub.project('Mission card');
  const LEAD = 'n_orchmiss01';
  // as narrow as a card gets: the pinned card must still fit
  await hub.ops(p.wid, [{ op: 'put', node: { id: LEAD, kind: 'orchestrator', x: 0, y: 0, w: 340, h: 640, data: { title: 'Lead' } } }]);
  // consoles that never take a turn: the orders wait with them, the tasks stay open
  const api = { ...(await hub.console(p.wid)), handle: '' };
  const web = { ...(await hub.console(p.wid)), handle: '' };
  api.handle = await handleOf(hub, api.id);
  web.handle = await handleOf(hub, web.id);
  // the card's team: the consoles wired to it
  await hub.ops(p.wid, [api, web].map((c, i) => ({ op: 'putEdge', edge: { id: `e_misswire0${i}`, kind: 'wire', source: LEAD, target: c.node, data: {} } })));
  let id = '';
  try {
    const m = await hub.call('POST', `/api/projects/${p.id}/missions`, { threadId: LEAD, cardId: LEAD, ...plan(api.handle, web.handle) });
    id = m.id;
    await page.goto(`/p/${p.id}/w/${p.wid}?card=${LEAD}`);
    const card = page.locator(`.react-flow__node[data-id="${LEAD}"]`);
    await expect(card.locator('.mission-chip')).toHaveText('Mission · 0/2', { timeout: 15_000 });
    const pinned = card.locator('.mission-card.is-pinned');
    await expect(pinned.locator('.mission-state')).toHaveText('Awaiting approval');
    const fits = await pinned.evaluate((el) => el.scrollWidth <= el.clientWidth + 1 && el.getBoundingClientRect().right <= el.closest('.react-flow__node')!.getBoundingClientRect().right + 1);
    expect(fits).toBe(true);

    await pinned.locator('.mission-actions .btn-primary').click();
    await expect(pinned.locator('.mission-state')).toHaveText('Running', { timeout: 15_000 });
    await expect.poll(async () => (await ordersTo(hub, api.id)).length, { timeout: 30_000 }).toBeGreaterThan(0);
    await expect(card.locator('.mission-chip')).toHaveText('Mission · 0/2');
    await expect(card.locator('.mission-chip')).toHaveClass(/tone-success/);
  } finally {
    if (id) await hub.call('POST', `/api/missions/${id}/stop`).catch(() => undefined);
    for (const c of [api, web]) await hub.call('DELETE', `/api/sessions/${c.id}`);
  }
});

test('the lead’s language is chosen in Settings and kept', async ({ page, hub }) => {
  await page.goto('/settings/lead');
  const lang = page.getByLabel('Language');
  await expect(lang).toHaveValue('');
  await expect(lang.locator('option')).toHaveText(['Automatic (follow my messages)', 'Bahasa Indonesia', 'English']);
  try {
    await lang.selectOption({ label: 'Bahasa Indonesia' });
    await expect.poll(async () => (await hub.call('GET', '/api/settings')).lead?.language).toBe('id');
    await page.reload();
    await expect(page.getByLabel('Language')).toHaveValue('id');

    // mission limits: a number, kept within bounds
    const rounds = page.getByLabel('Rounds');
    await rounds.fill('12');
    await rounds.press('Enter');
    await expect.poll(async () => (await hub.call('GET', '/api/settings')).lead?.missionRounds).toBe(12);
  } finally {
    await hub.call('PATCH', '/api/settings', { lead: { language: null, missionRounds: null } });
  }
});

test('a mission tile: the owner writes a mission, it starts as written, and is found in the history to run again', async ({ page, hub }) => {
  test.setTimeout(120_000);
  const p = await hub.project('Mission tile');
  const TILE = 'n_misstile01';
  await hub.ops(p.wid, [{ op: 'put', node: { id: TILE, kind: 'orchestrator', x: 0, y: 0, w: 460, h: 760, data: { title: 'Mission', view: 'mission' } } }]);
  const api = { ...(await hub.console(p.wid)), handle: '' };
  const web = { ...(await hub.console(p.wid)), handle: '' };
  api.handle = await handleOf(hub, api.id);
  web.handle = await handleOf(hub, web.id);
  await hub.ops(p.wid, [api, web].map((c, i) => ({ op: 'putEdge', edge: { id: `e_tilewire0${i}`, kind: 'wire', source: TILE, target: c.node, data: {} } })));
  try {
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    const tile = page.locator(`.react-flow__node[data-id="${TILE}"]`);
    await expect(tile.locator('.mission-empty')).toContainText('No mission running', { timeout: 15_000 });
    await expect(tile.locator('.mission-tile-foot')).toContainText('Team: 2 consoles wired');

    // the form: goal, a task per console, the second after the first
    await tile.getByRole('button', { name: 'New mission' }).click();
    await tile.getByRole('button', { name: 'Start mission' }).click();
    await expect(tile.getByRole('alert')).toHaveText('Write the goal.');
    await tile.getByLabel('Goal').fill('Build the sign-up form and its endpoint');
    await tile.getByLabel('Console for t1').selectOption(api.handle);
    await tile.getByLabel('What t1 should do').fill('Write POST /signup with a test.');
    await tile.getByRole('button', { name: 'Add task' }).click();
    await tile.getByLabel('Console for t2').selectOption(web.handle);
    await tile.getByLabel('What t2 should do').fill('Build the form that posts to /signup.');
    await tile.getByRole('group', { name: 't2 starts after' }).getByRole('button', { name: 't1' }).click();
    await tile.getByRole('button', { name: 'Start mission' }).click();

    // started as written: no approval left, the first task went out, the second waits for it
    const card = tile.locator('.mission-card.is-pinned');
    await expect(card.locator('.mission-state')).toHaveText('Running', { timeout: 15_000 });
    await expect(tile.locator('.mission-chip')).toContainText('Running · 0/2');
    await expect.poll(async () => (await ordersTo(hub, api.id)).some((t) => t.includes('Write POST /signup')), { timeout: 30_000 }).toBe(true);
    const runs = (await hub.call('GET', `/api/projects/${p.id}/missions?active=1`)).items;
    expect(runs[0]).toMatchObject({ threadId: TILE, cardId: TILE, state: 'running' });
    expect(runs[0].tasks[1]).toMatchObject({ id: 't2', title: 'Build the form that posts to /signup.', dependsOn: ['t1'], state: 'waiting' });
    await expect(card.getByRole('list', { name: 'Tasks per agent' })).toContainText(`@${web.handle}`);

    // stopped, it is in the history, and Run again fills the form with it
    await card.getByRole('button', { name: 'Stop mission' }).click();
    await page.getByRole('dialog', { name: 'Stop the mission?' }).getByRole('button', { name: 'Stop mission' }).click();
    await expect(tile.locator('.mission-empty')).toContainText('No mission running', { timeout: 15_000 });
    await tile.getByRole('radio', { name: /^History/ }).click();
    const row = tile.locator('.mission-history-row').first();
    await expect(row).toContainText('Build the sign-up form and its endpoint');
    await expect(row.locator('.mission-state')).toHaveText('Stopped');
    await row.locator('.mission-history-head').click();
    await row.getByRole('button', { name: 'Run again' }).click();
    await expect(tile.getByLabel('Goal')).toHaveValue('Build the sign-up form and its endpoint');
    await expect(tile.getByLabel('What t2 should do')).toHaveValue('Build the form that posts to /signup.');
  } finally {
    for (const c of [api, web]) await hub.call('DELETE', `/api/sessions/${c.id}`);
  }
});
