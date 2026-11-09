package studio

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/azamrizki/nextboard/internal/machine"
)

const webRoutes = `<?php
use App\Http\Controllers\UserController;
use Illuminate\Support\Facades\Route;

// Route::get('/commented', fn () => 1)->name('commented');
/* Route::get('/blocked')->name('blocked'); */
Route::get('/', function () {
    return view('welcome');
})->name('home');

Route::post('/login', [AuthController::class, 'login'])->name('login');
Route::view('/about', 'pages.about')->name('about');
Route::match(['get', 'post'], '/both', [BothController::class, 'go'])->name('both');
Route::get("/double/{id}", 'Legacy@show')->name("double.show");
Route::get('/nameless', [PlainController::class, 'x']);

Route::middleware(['auth'])->prefix('admin')->name('admin.')->group(function () {
    Route::get('/dashboard', [AdminController::class, 'index'])->name('dashboard');
    Route::resource('users', UserController::class);
    Route::prefix('reports')->name('reports.')->group(function () {
        Route::get('/daily', [ReportController::class, 'daily'])->name('daily');
    });
});

Route::resource('photos', PhotoController::class)->only(['index', 'show']);
Route::resource('tags', TagController::class)->except(['destroy']);
Route::apiResource('categories', CategoryController::class);
Route::get('/after', fn () => 'x')->name('after.group');
Route::name('quoted.')->group(function () { Route::get('/q', fn () => 'it\'s; }')->name('s'); });
`

func routeByName(rs []LaravelRoute) map[string]LaravelRoute {
	out := map[string]LaravelRoute{}
	for _, r := range rs {
		if r.Name != "" {
			out[r.Name] = r
		}
	}
	return out
}

func TestParseRoutes(t *testing.T) {
	rs := parseRoutes(webRoutes, "routes/web.php")
	got := routeByName(rs)
	for name, want := range map[string]struct{ method, uri string }{
		"home":                {"GET", "/"},
		"login":               {"POST", "login"},
		"about":               {"GET", "about"},
		"both":                {"GET|POST", "both"},
		"double.show":         {"GET", "double/{id}"},
		"admin.dashboard":     {"GET", "admin/dashboard"},
		"admin.users.index":   {"GET", "admin/users"},
		"admin.users.update":  {"PUT|PATCH", "admin/users/{user}"},
		"admin.users.destroy": {"DELETE", "admin/users/{user}"},
		"admin.reports.daily": {"GET", "admin/reports/daily"},
		"photos.index":        {"GET", "photos"},
		"photos.show":         {"GET", "photos/{photo}"},
		"tags.edit":           {"GET", "tags/{tag}/edit"},
		"categories.store":    {"POST", "categories"},
		"after.group":         {"GET", "after"},
		"quoted.s":            {"GET", "q"},
	} {
		r, ok := got[name]
		if !ok {
			t.Errorf("no route %q", name)
			continue
		}
		if r.Method != want.method || r.URI != want.uri {
			t.Errorf("%s = %s %s, want %s %s", name, r.Method, r.URI, want.method, want.uri)
		}
	}
	for _, not := range []string{"commented", "blocked", "photos.create", "photos.destroy", "tags.destroy", "categories.create", "categories.edit"} {
		if _, ok := got[not]; ok {
			t.Errorf("route %q should not exist", not)
		}
	}
	if r := got["login"]; r.Line != 11 || r.File != "routes/web.php" || r.Action != "AuthController@login" {
		t.Errorf("login: %+v", r)
	}
	if !slices.ContainsFunc(rs, func(r LaravelRoute) bool { return r.URI == "nameless" && r.Name == "" }) {
		t.Error("a route without a name is still a route")
	}
	if got["home"].Action != "Closure" {
		t.Errorf("home action %q", got["home"].Action)
	}
}

func TestParseRoutesApiPrefix(t *testing.T) {
	got := routeByName(parseRoutes(`<?php Route::get('/users', [A::class, 'b'])->name('api.users');`, "routes/api.php"))
	if got["api.users"].URI != "api/users" {
		t.Fatalf("%+v", got)
	}
}

const appConfig = `<?php

use Illuminate\Support\Facades\Facade;

return [
    // a comment with 'fake' => 'value'
    'name' => env('APP_NAME', 'Laravel'),
    'debug' => (bool) env('APP_DEBUG', false),
    'providers' => [
        App\Providers\AppServiceProvider::class,
    ],
    'mail' => [
        'from' => [
            'address' => env('MAIL_FROM_ADDRESS', 'hello@example.com'),
            'name' => 'x',
        ],
    ],
    'callback' => fn () => ['inner' => 1],
    'old' => array(
        'style' => true,
    ),
];
`

func TestConfigKeys(t *testing.T) {
	keys := phpArrayKeys(appConfig, "config/app.php", "app")
	got := map[string]int{}
	for _, k := range keys {
		got[k.Key] = k.Line
	}
	for _, want := range []string{"app.name", "app.debug", "app.providers", "app.mail", "app.mail.from", "app.mail.from.address", "app.mail.from.name", "app.callback", "app.old", "app.old.style"} {
		if _, ok := got[want]; !ok {
			t.Errorf("no key %s in %v", want, got)
		}
	}
	for _, not := range []string{"app.fake", "app.inner", "app.callback.inner"} {
		if _, ok := got[not]; ok {
			t.Errorf("key %s should not exist", not)
		}
	}
	if got["app.name"] != 7 {
		t.Errorf("line of app.name = %d", got["app.name"])
	}
}

func TestLogEntries(t *testing.T) {
	out := "storage/logs/laravel.log\n99\n" +
		"half of an entry cut by the read\n" +
		"[2026-11-09 10:00:00] production.INFO: started\n" +
		"[2026-11-09 10:00:01] production.ERROR: Division by zero {\"exception\":\"[object] (DivisionByZeroError(code: 0): Division by zero at /srv/app/app/Http/Foo.php:12)\n" +
		"[stacktrace]\n" +
		"#0 /srv/app/vendor/laravel/framework/Router.php(100): x()\n" +
		"#1 /srv/app/app/Http/Bar.php(5): y()\n" +
		"\"}\n" +
		"[2026-11-09T10:00:02.000+07:00] local.WARNING: only in vendor at /srv/app/vendor/x/y.php:3\n"
	log := parseLaravelLog(out, "/srv/app")
	if log.File != "storage/logs/laravel.log" || log.Cut || len(log.Entries) != 3 {
		t.Fatalf("%+v", log)
	}
	if log.Entries[0].Level != "WARNING" || log.Entries[0].Env != "local" || log.Entries[0].File != "" {
		t.Errorf("newest: %+v", log.Entries[0])
	}
	e := log.Entries[1]
	if e.Level != "ERROR" || e.File != "app/Http/Foo.php" || e.Line != 12 || !strings.Contains(e.Trace, "Bar.php") {
		t.Errorf("error: %+v", e)
	}
	if log.Entries[2].Message != "started" {
		t.Errorf("oldest: %+v", log.Entries[2])
	}
	if got := parseLaravelLog("", "/x"); len(got.Entries) != 0 || got.File != "" {
		t.Errorf("no log: %+v", got)
	}
}

func TestClassComponentName(t *testing.T) {
	if got := classComponentName("app/View/Components/Form/TextInput.php"); got != "form.text-input" {
		t.Fatal(got)
	}
}

// laravelApp writes a small app, the real file layout, and only that.
func laravelApp(t *testing.T, root string) {
	t.Helper()
	for rel, body := range map[string]string{
		"artisan":                                    "<?php\n",
		"composer.json":                              `{"require":{"php":"^8.2","laravel/framework":"^11.0"}}`,
		"routes/web.php":                             webRoutes,
		"routes/console.php":                         `<?php Route::get('/not-a-route', fn () => 1)->name('console.thing');`,
		"config/app.php":                             appConfig,
		"lang/en/auth.php":                           "<?php return ['failed' => 'No.'];",
		"resources/views/welcome.blade.php":          "<h1>hi</h1>",
		"resources/views/pages/about.blade.php":      "<p>about</p>",
		"resources/views/components/alert.blade.php": "<div></div>",
		"resources/views/not-a-view.txt":             "x",
		"app/View/Components/Form/TextInput.php":     "<?php class TextInput {}",
		".env":                                       "APP_KEY=base64:CANARY-from-the-dot-env\nSECRET_ONLY_HERE=1\n",
		".env.example":                               "APP_NAME=Laravel\nAPP_KEY=\n# COMMENTED=1\nDB_HOST=127.0.0.1\n",
		"storage/logs/laravel-2026-11-08.log":        "[2026-11-08 10:00:00] production.INFO: older day\n",
		"storage/logs/laravel-2026-11-09.log":        "[2026-11-09 10:00:00] production.ERROR: newest day\n",
	} {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	older := filepath.Join(root, "storage/logs/laravel-2026-11-08.log")
	_ = exec.Command("touch", "-d", "2026-11-08", older).Run()
}

func TestScanLaravelProject(t *testing.T) {
	root := t.TempDir()
	laravelApp(t, root)
	h := machine.NewLocal(machine.Config{})
	idx, err := ScanLaravel(context.Background(), h, root)
	if err != nil {
		t.Fatal(err)
	}
	if !idx.Detected || idx.Root != "" || idx.Version != "^11.0" {
		t.Fatalf("%+v", idx)
	}
	views := []string{}
	for _, v := range idx.Views {
		views = append(views, v.Name)
	}
	if !slices.Equal(views, []string{"components.alert", "pages.about", "welcome"}) {
		t.Errorf("views %v", views)
	}
	comps := []string{}
	for _, c := range idx.Components {
		comps = append(comps, c.Name)
	}
	if !slices.Equal(comps, []string{"alert", "form.text-input"}) {
		t.Errorf("components %v", comps)
	}
	if _, ok := routeByName(idx.Routes)["admin.users.index"]; !ok {
		t.Errorf("routes %v", idx.Routes)
	}
	if _, ok := routeByName(idx.Routes)["console.thing"]; ok {
		t.Error("console.php holds no routes")
	}
	if !slices.ContainsFunc(idx.Config, func(k LaravelKey) bool { return k.Key == "app.mail.from.address" }) {
		t.Errorf("config %v", idx.Config)
	}
	if !slices.ContainsFunc(idx.Lang, func(k LaravelKey) bool { return k.Key == "auth.failed" }) {
		t.Errorf("lang %v", idx.Lang)
	}
	if !slices.Equal(idx.Env, []string{"APP_NAME", "APP_KEY", "DB_HOST"}) {
		t.Errorf("env %v", idx.Env)
	}
	// The real .env is not even read: nothing of it can be in the answer.
	raw, _ := machine.Exec(context.Background(), h, machine.ExecSpec{Cwd: root, Script: "exec /bin/sh -c " + machine.Quote(laravelFinder)})
	if strings.Contains(string(raw.Stdout), "CANARY-from-the-dot-env") || strings.Contains(string(raw.Stdout), "SECRET_ONLY_HERE") {
		t.Fatal(".env was read")
	}
	log, err := ReadLaravelLog(context.Background(), h, root)
	if err != nil || len(log.Entries) != 1 || log.Entries[0].Message != "newest day" {
		t.Fatalf("log: %+v %v", log, err)
	}
}

func TestScanFindsTheAppOneFolderDown(t *testing.T) {
	root := t.TempDir()
	laravelApp(t, filepath.Join(root, "backend"))
	idx, err := ScanLaravel(context.Background(), machine.NewLocal(machine.Config{}), root)
	if err != nil || !idx.Detected || idx.Root != "backend" {
		t.Fatalf("%+v %v", idx, err)
	}
}

func TestScanOtherProjects(t *testing.T) {
	root := t.TempDir()
	h := machine.NewLocal(machine.Config{})
	idx, err := ScanLaravel(context.Background(), h, root)
	if err != nil || idx.Detected {
		t.Fatalf("empty: %+v %v", idx, err)
	}
	// an artisan file alone is no Laravel app
	if err := os.WriteFile(filepath.Join(root, "artisan"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "composer.json"), []byte(`{"require":{"symfony/console":"*"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if idx, err := ScanLaravel(context.Background(), h, root); err != nil || idx.Detected {
		t.Fatalf("not laravel: %+v %v", idx, err)
	}
}

// The real php, with an artisan that does what a real one does at the door: a notice, then JSON.
func TestArtisanRoutes(t *testing.T) {
	if _, err := exec.LookPath("php"); err != nil {
		t.Skip("no php")
	}
	root := t.TempDir()
	h := machine.NewLocal(machine.Config{})
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "artisan"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`<?php
if ($argv[1] !== 'route:list' || !in_array('--json', $argv)) { fwrite(STDERR, "unexpected call"); exit(9); }
echo "Deprecated: something\n";
echo json_encode([['method' => 'GET|HEAD', 'uri' => 'users', 'name' => 'users.index', 'action' => 'App\\UserController@index', 'middleware' => ['web']]]);
`)
	got, err := ArtisanRoutes(context.Background(), h, root)
	if err != nil || len(got) != 1 || got[0].Name != "users.index" || got[0].Method != "GET|HEAD" {
		t.Fatalf("%+v %v", got, err)
	}
	write(`<?php fwrite(STDERR, "Could not connect to the database"); exit(1);`)
	if _, err := ArtisanRoutes(context.Background(), h, root); err == nil || !strings.Contains(err.Error(), "Could not connect") {
		t.Fatalf("failure: %v", err)
	}
	write(`<?php echo "not json at all";`)
	if _, err := ArtisanRoutes(context.Background(), h, root); err == nil {
		t.Fatal("nonsense was taken for routes")
	}
}
