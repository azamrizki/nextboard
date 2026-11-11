import { expect, test } from './hub';

// Answering an agent's result from the thread is an order too: the agent's next result comes
// back to the thread, whether the answer is typed with the message's Reply or in its output window.

test('an answer from the output window brings the next result back to the thread', async ({ page, hub }) => {
  test.setTimeout(90_000);
  const p = await hub.project('Thread reply');
  const s = await hub.call('POST', '/api/sessions', { agent: 'fakeworker', workspaceId: p.wid });
  const handle: string = s.handle;
  try {
    await page.goto(`/p/${p.id}/w/${p.wid}`);
    const bar = page.getByRole('combobox', { name: 'Order for your agents' });
    await expect(bar).toBeVisible();
    await expect.poll(async () => (await hub.call('GET', `/api/sessions/${s.id}`)).agentState, { timeout: 20_000 }).toBe('ready');
    await bar.fill(`@${handle} run the tests`);
    await bar.press('Enter');

    const thread = page.getByRole('region', { name: 'Thread' });
    const outputs = thread.getByRole('button', { name: /^See output/ });
    await expect(outputs).toHaveCount(1, { timeout: 30_000 });

    // the answer, from the output window
    await outputs.first().click();
    const reply = page.getByRole('textbox', { name: `Reply to @${handle}` });
    await expect(reply).toBeVisible();
    await reply.fill('now fix the failing one');
    await page.getByRole('button', { name: 'Send reply' }).click();
    // the window closes on the thread, which shows the answer going out as an order
    await expect(reply).toBeHidden();
    await expect(thread.getByText(`Sent to @${handle}`, { exact: false }).last()).toBeVisible();

    // and the agent's next result is reported in the thread
    await expect(outputs).toHaveCount(2, { timeout: 30_000 });
    await expect(thread.getByText('now fix the failing one', { exact: false }).first()).toBeVisible();
  } finally {
    await hub.call('DELETE', `/api/sessions/${s.id}`);
  }
});
