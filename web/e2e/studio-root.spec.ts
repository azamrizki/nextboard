import type { Page, Request } from '@playwright/test';
import { expect, test } from './hub';
import { twoParts } from './two-parts';

// Documents and Diagrams in a project of several parts read it at its root by default, every part
// (docs/API-studio.md §5, §6): "Read in: Root / <part>…", Root picked, and the request says
// root: true; a part picked goes to that part's ref. A project in one folder never asks. The runs
// never start: the test answers the generate request itself, so no CLI is ever run.

/** Answers every generate request to `kind` with an error, and keeps what was sent. */
async function catchGenerate(page: Page, kind: 'diagrams' | 'documents') {
  const sent: { url: string; body: Record<string, unknown> }[] = [];
  await page.route(`**/api/projects/*/${kind}`, async (route) => {
    const req: Request = route.request();
    if (req.method() !== 'POST') return route.continue();
    sent.push({ url: new URL(req.url()).pathname, body: req.postDataJSON() });
    await route.fulfill({ status: 400, contentType: 'application/json', body: JSON.stringify({ error: 'the test stops it here', code: 'runner_unsupported' }) });
  });
  return sent;
}

async function openStudio(page: Page, name: 'Diagrams' | 'Documents') {
  await page.getByRole('button', { name: 'Studios', exact: true }).first().click();
  await page.getByRole('menuitem', { name }).click();
}

test('Diagrams and Documents read every part by default, or the part picked', async ({ page, hub }) => {
  const p = await twoParts(hub, 'Atlas');
  const diagrams = await catchGenerate(page, 'diagrams');
  const documents = await catchGenerate(page, 'documents');
  await page.goto(`/p/${p.id}/w/${p.wid}`);

  await openStudio(page, 'Diagrams');
  const panel = page.getByRole('complementary', { name: 'Diagrams' });
  const place = panel.getByRole('radiogroup', { name: 'Read in' });
  await expect(place.getByRole('radio')).toHaveText(['Root', 'admin', 'frontend']);
  await expect(place.getByRole('radio', { name: 'Root' })).toHaveAttribute('aria-checked', 'true');
  await expect(panel).toContainText('Reads the project root (every part).');
  await panel.getByRole('button', { name: 'Draw diagram' }).click();
  await expect.poll(() => diagrams.length).toBe(1);
  expect(diagrams[0].url).toBe(`/api/projects/${p.id}/diagrams`);
  expect(diagrams[0].body).toMatchObject({ subject: 'architecture', root: true });

  // a part: its ref, no root
  await place.getByRole('radio', { name: 'frontend' }).click();
  await expect(panel).toContainText(`Reads ${p.front.path}.`);
  await panel.getByRole('button', { name: 'Draw diagram' }).click();
  await expect.poll(() => diagrams.length).toBe(2);
  expect(diagrams[1].url).toBe(`/api/projects/${p.id}~${p.front.id}/diagrams`);
  expect(diagrams[1].body.root).toBeUndefined();
  await panel.getByRole('button', { name: 'Close diagrams' }).click();

  await openStudio(page, 'Documents');
  const studio = page.getByRole('dialog', { name: 'Documents' });
  const docPlace = studio.getByRole('radiogroup', { name: 'Read in' });
  await expect(docPlace.getByRole('radio', { name: 'Root' })).toHaveAttribute('aria-checked', 'true');
  await expect(studio).toContainText('A hidden, read-only run of your CLI in the project root (every part)');
  await studio.getByRole('button', { name: 'Generate', exact: true }).click();
  await expect.poll(() => documents.length).toBe(1);
  expect(documents[0].url).toBe(`/api/projects/${p.id}/documents`);
  expect(documents[0].body).toMatchObject({ type: 'overview', root: true });
  await docPlace.getByRole('radio', { name: 'admin' }).click();
  await expect(studio).toContainText(`A hidden, read-only run of your CLI in ${p.admin.path}`);
  await studio.getByRole('button', { name: 'Generate', exact: true }).click();
  await expect.poll(() => documents.length).toBe(2);
  expect(documents[1].url).toBe(`/api/projects/${p.id}~${p.admin.id}/documents`);
  expect(documents[1].body.root).toBeUndefined();
});

test('a project in one folder is read there, with no question', async ({ page, hub }) => {
  const p = await hub.project('Solo');
  const diagrams = await catchGenerate(page, 'diagrams');
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await openStudio(page, 'Diagrams');
  const panel = page.getByRole('complementary', { name: 'Diagrams' });
  await expect(panel.getByRole('button', { name: 'Draw diagram' })).toBeVisible();
  await expect(panel.getByRole('radiogroup', { name: 'Read in' })).toHaveCount(0);
  await expect(panel).not.toContainText('Reads ');
  await panel.getByRole('button', { name: 'Draw diagram' }).click();
  await expect.poll(() => diagrams.length).toBe(1);
  expect(diagrams[0].url).toBe(`/api/projects/${p.id}/diagrams`);
  expect(diagrams[0].body.root).toBeUndefined();
});
