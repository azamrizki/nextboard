import { mkdirSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { expect, test } from './hub';

const WEB = `<?php
use Illuminate\\Support\\Facades\\Route;

Route::get('/', fn () => view('welcome'))->name('home');
Route::middleware('auth')->prefix('admin')->name('admin.')->group(function () {
    Route::resource('users', UserController::class);
});
`;

function app(root: string) {
  const files: Record<string, string> = {
    // a real php answers this one: what artisan does at the door, a notice and then JSON
    artisan: `<?php echo "Deprecated: noise\\n"; echo json_encode([['method' => 'GET|HEAD', 'uri' => 'from/artisan', 'name' => 'artisan.only', 'action' => 'X@y']]);\n`,
    'composer.json': '{"require":{"laravel/framework":"^12.0"}}',
    'routes/web.php': WEB,
    'config/app.php': "<?php\nreturn [\n    'name' => 'Demo',\n    'mail' => ['from' => 'a@example.com'],\n];\n",
    'resources/views/welcome.blade.php': "<div>{{ $name }}</div>\n@include('')\n",
    'resources/views/pages/about.blade.php': '<p>about</p>\n',
    '.env': 'APP_KEY=CANARY-laravel-dot-env\n',
    '.env.example': 'APP_NAME=Demo\n',
    'storage/logs/laravel.log': '[2026-11-09 10:00:00] production.INFO: quiet line\n[2026-11-09 10:00:01] production.ERROR: Something broke at ' + root + '/app/Foo.php:12\n[stacktrace]\n#0 frame\n',
    'app/Foo.php': "<?php\n// 2\n// 3\n// 4\n// 5\n// 6\n// 7\n// 8\n// 9\n// 10\n// 11\nthrow new Exception('here');\n",
    'app/Http/Bar.php': "<?php\nreturn \n",
  };
  for (const [rel, body] of Object.entries(files)) {
    mkdirSync(dirname(join(root, rel)), { recursive: true });
    writeFileSync(join(root, rel), body);
  }
}

test('a Laravel project gets its view and an editor that knows its names', async ({ page, hub }, info) => {
  test.slow();
  const p = await hub.project(`Laravel ${info.project.name}`);
  app(p.path);
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_laravel001', kind: 'editor', x: 0, y: 0, w: 1300, h: 800, data: {} } }]);
  const answers: string[] = [];
  page.on('response', async (r) => {
    if (new URL(r.url()).pathname.startsWith(`/api/projects/${p.id}/laravel`)) answers.push(await r.text().catch(() => ''));
  });

  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_laravel001`);
  const view = page.getByRole('button', { name: 'Laravel', exact: true });
  await expect(view).toBeVisible({ timeout: 20_000 });
  await view.click();
  const side = page.locator('.wb-side');

  // routes from the files, group prefixes and resources included
  await expect(side.getByText('admin/users/{user}/edit')).toBeVisible();
  await side.getByRole('textbox', { name: 'Filter routes' }).fill('admin.users.destroy');
  const row = side.locator('.wb-lh-row', { hasText: 'admin/users' });
  await expect(row).toHaveCount(1);

  // a click goes to where the route is set
  await row.click();
  await expect(page.locator('.wb-tab', { hasText: 'web.php' })).toBeVisible({ timeout: 10_000 });
  // the opened file takes the focus a moment later, and may take it in the middle of a fill
  const filter = side.getByRole('textbox', { name: 'Filter routes' });
  await expect(async () => {
    await filter.fill('');
    await expect(filter).toHaveValue('', { timeout: 500 });
  }).toPass({ timeout: 10_000 });
  await expect(side.getByText('admin/users/{user}/edit')).toBeVisible();

  // the log: problems only, with a way to the line
  await expect(side.getByText('Something broke', { exact: false })).toBeVisible();
  await expect(side.getByText('quiet line', { exact: false })).toHaveCount(0);
  await side.getByText('Something broke', { exact: false }).click();
  await side.getByRole('button', { name: 'app/Foo.php:12', exact: true }).click();
  await expect(page.locator('.wb-tab', { hasText: 'Foo.php' })).toBeVisible({ timeout: 10_000 });

  // artisan asks first, and what it says replaces the list
  await side.getByRole('button', { name: 'Ask artisan' }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText('boots the application');
  await dialog.getByRole('button', { name: 'Run route:list' }).click();
  await expect(side.getByText('from/artisan')).toBeVisible({ timeout: 20_000 });

  // an artisan command is typed, not run
  await side.getByRole('button', { name: 'migrate:status' }).click();
  const term = page.locator('.wb-terminal .xterm-rows');
  await expect(term).toContainText('php artisan migrate:status', { timeout: 15_000 });

  // the editor completes names from the project
  await page.getByRole('button', { name: 'Explorer' }).click();
  await side.getByRole('treeitem', { name: 'app' }).click();
  await side.getByRole('treeitem', { name: 'Http' }).click();
  await side.getByRole('treeitem', { name: 'Bar.php' }).click();
  await expect(page.locator('.wb-tab', { hasText: 'Bar.php' })).toBeVisible();
  await page.locator('.monaco-editor .view-line', { hasText: 'return' }).click();
  await page.keyboard.press('End');
  await page.keyboard.type("route('admin.users.");
  const suggest = page.locator('.suggest-widget');
  await expect(suggest.getByText('admin.users.index').first()).toBeVisible({ timeout: 10_000 });
  await page.keyboard.press('Escape');
  await page.keyboard.press('Control+a');
  await page.keyboard.type("<?php return config('app.mail.");
  await expect(suggest.getByText('app.mail.from').first()).toBeVisible({ timeout: 10_000 });
  await page.keyboard.press('Escape');

  // and a view name takes you to its file
  await page.keyboard.press('Control+a');
  await page.keyboard.type("<?php return view('pages.about');");
  await page.keyboard.press('Escape');
  await page.keyboard.press('Home');
  for (let i = 0; i < 20; i++) await page.keyboard.press('ArrowRight');
  await page.keyboard.press('F12');
  await expect(page.locator('.wb-tab', { hasText: 'about.blade.php' })).toBeVisible({ timeout: 10_000 });

  // Blade has its language
  await side.getByRole('treeitem', { name: 'resources' }).click();
  await side.getByRole('treeitem', { name: 'views' }).click();
  await side.getByRole('treeitem', { name: 'welcome.blade.php' }).click();
  await expect(page.locator('.wb-status').getByText('Blade', { exact: true })).toBeVisible({ timeout: 10_000 });

  // nothing the hub said carries what .env holds
  expect(answers.length).toBeGreaterThan(0);
  expect(answers.join('')).not.toContain('CANARY-laravel-dot-env');
});

test('other projects see no Laravel view and ask nothing more of it', async ({ page, hub }, info) => {
  const p = await hub.project(`Plain ${info.project.name}`);
  writeFileSync(join(p.path, 'README.md'), '# plain\n');
  await hub.ops(p.wid, [{ op: 'put', node: { id: 'n_laravel002', kind: 'editor', x: 0, y: 0, w: 1100, h: 700, data: {} } }]);
  const failed: string[] = [];
  page.on('response', (r) => {
    if (r.status() >= 400 && new URL(r.url()).pathname.includes('/laravel')) failed.push(r.url());
  });
  await page.goto(`/p/${p.id}/w/${p.wid}?card=n_laravel002`);
  await expect(page.getByRole('button', { name: 'Explorer' })).toBeVisible({ timeout: 20_000 });
  await page.waitForTimeout(800);
  await expect(page.getByRole('button', { name: 'Laravel', exact: true })).toHaveCount(0);
  expect(failed).toEqual([]);
});
