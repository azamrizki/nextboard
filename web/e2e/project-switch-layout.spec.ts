import { expect, test } from './hub';

// On a tablet the project switcher of the Code view is a sheet over the page, nearly the size of
// the screen, with the same margin on every side (DESIGN.md §3.19); elsewhere it hangs from its
// button as before.
test('the project switcher is a sheet on a tablet', async ({ page, hub }, info) => {
  const p = await hub.project('Switcher');
  await page.goto(`/p/${p.id}/code`);
  await page.getByRole('button', { name: /Switch project/ }).first().click();
  const menu = page.getByRole('menu', { name: 'Switch project' });
  await expect(menu).toBeVisible();
  const box = (await menu.boundingBox())!;
  const view = page.viewportSize()!;
  if (info.project.name.startsWith('tablet')) {
    const margins = [box.x, box.y, view.width - box.x - box.width, view.height - box.y - box.height].map(Math.round);
    expect(new Set(margins).size, `margins ${margins}`).toBe(1);
    expect(margins[0]).toBeGreaterThan(8);
    expect(margins[0]).toBeLessThan(40);
  } else {
    expect(box.height).toBeLessThan(view.height * 0.8);
  }
  await page.keyboard.press('Escape');
  await expect(menu).toHaveCount(0);
});
