// The made-up projects of the demo hub (setup.ts). Every name, file, person and commit here is
// invented; the people's addresses are at example.com.

export interface DemoProject {
  slug: string;
  name: string;
  files: Record<string, string>;
  commits?: { author: string; message: string; files?: Record<string, string> }[];
  /** Left uncommitted, for the Source Control view. */
  dirty?: Record<string, string>;
  /** What the tester's canned work writes into the folder (agent.sh). */
  patch?: Record<string, string>;
  memory?: string;
  pages?: { name: string; title: string; description?: string; content: string }[];
  /** Its cover and icon, painted at run time (demo.ts paintArt): no picture file, no network. */
  art?: Art;
  /** The last commits not on its remote yet (each repository has a bare "origin" in the demo home). */
  unpushed?: number;
  /** Archived after it is added, for the Archived view. */
  archived?: boolean;
  id?: string;
  wid?: string;
}

/** A gradient with a motif drawn over it (demo.ts paintArt), the cover 3:1 and the icon square. */
export interface Art {
  colors: [string, string, string];
  motif: 'bag' | 'pages' | 'phone' | 'shapes' | 'bars' | 'nodes' | 'spark' | 'window' | 'gift';
  seed: number;
}

/** A part of a project in a folder on another machine (ROADMAP #49), on the demo's SSH server. */
export interface DemoPart {
  project: string;
  name: string;
  /** The machine's name in the hub. */
  machine: string;
  slug: string;
  files: Record<string, string>;
  commits: { author: string; message: string; files?: Record<string, string> }[];
  unpushed?: number;
}

const checkout = `import { Cart, subtotal } from './cart';
import { shippingFor } from './shipping/rates';

export interface Summary {
  subtotal: number;
  shipping: number;
  total: number;
}

/** What the order summary shows, in cents. */
export function summarize(cart: Cart): Summary {
  const sub = subtotal(cart);
  const shipping = shippingFor(sub);
  return { subtotal: sub, shipping, total: sub + shipping };
}
`;

const checkoutWithCoupons = `import { Cart, subtotal } from './cart';
import { shippingFor } from './shipping/rates';
import { Coupon, findCoupon } from './coupons';

export interface Summary {
  subtotal: number;
  discount: number;
  shipping: number;
  total: number;
  coupon?: string;
}

/** What the order summary shows, in cents. */
export function summarize(cart: Cart, code?: string, now = new Date()): Summary {
  const sub = subtotal(cart);
  const coupon = code ? findCoupon(code, now) : undefined;
  const discount = coupon ? discountFor(coupon, sub) : 0;
  const shipping = shippingFor(sub - discount);
  return { subtotal: sub, discount, shipping, total: sub - discount + shipping, coupon: coupon?.code };
}

function discountFor(coupon: Coupon, sub: number): number {
  if (coupon.kind === 'percent') return Math.round((sub * coupon.value) / 100);
  return Math.min(coupon.value, sub);
}
`;

const cart = `export interface Line {
  sku: string;
  name: string;
  price: number; // cents
  qty: number;
}

export interface Cart {
  lines: Line[];
}

export function subtotal(cart: Cart): number {
  return cart.lines.reduce((sum, l) => sum + l.price * l.qty, 0);
}
`;

const rates = `/** Free shipping from $50, else a flat rate. In cents. */
export const FREE_FROM = 5000;
export const FLAT = 499;

export function shippingFor(subtotal: number): number {
  return subtotal >= FREE_FROM ? 0 : FLAT;
}
`;

export const PROJECTS: DemoProject[] = [
  {
    slug: 'acme-shop',
    name: 'acme-shop',
    art: { colors: ['#3b1d5e', '#c2410c', '#f59e0b'], motif: 'bag', seed: 11 },
    unpushed: 1,
    files: {
      'README.md': '# acme-shop\n\nThe Acme storefront: catalog, cart and checkout.\n\n```bash\nnpm install\nnpm run dev   # http://localhost:4173\nnpm test\n```\n',
      'package.json': JSON.stringify({ name: 'acme-shop', private: true, type: 'module', scripts: { dev: 'vite', build: 'vite build', test: 'vitest run', typecheck: 'tsc --noEmit' }, devDependencies: { typescript: '^5.9.0', vite: '^7.1.0', vitest: '^3.2.0' } }, null, 2) + '\n',
      'tsconfig.json': JSON.stringify({ compilerOptions: { target: 'ES2022', module: 'ESNext', moduleResolution: 'bundler', strict: true, noEmit: true }, include: ['src'] }, null, 2) + '\n',
      'src/cart.ts': cart,
    },
    commits: [
      { author: 'Rina Hart', message: 'feat: catalog and cart', files: { 'src/catalog.ts': "export const categories = ['New', 'Kitchen', 'Outdoor', 'Sale'];\n" } },
      { author: 'Sam Okafor', message: 'feat: checkout summary', files: { 'src/checkout.ts': checkout } },
      { author: 'Rina Hart', message: 'feat: free shipping from $50', files: { 'src/shipping/rates.ts': rates } },
      { author: 'Lee Park', message: 'test: shipping rates', files: { 'src/shipping/rates.test.ts': "import { expect, test } from 'vitest';\nimport { shippingFor } from './rates';\n\ntest('free from $40', () => {\n  expect(shippingFor(4000)).toBe(0);\n  expect(shippingFor(3999)).toBe(499);\n});\n" } },
      { author: 'Sam Okafor', message: 'feat: account page skeleton', files: { 'src/account/page.tsx': "export function AccountPage() {\n  return (\n    <main>\n      <h1>Your account</h1>\n    </main>\n  );\n}\n", 'src/api/orders.ts': "export interface Order {\n  id: string;\n  placedAt: string;\n  total: number;\n}\n\nexport async function listOrders(customer: string): Promise<Order[]> {\n  const r = await fetch(`/api/customers/${customer}/orders`);\n  return r.json();\n}\n" } },
    ],
    patch: {
      'src/checkout.ts': checkoutWithCoupons,
      'src/coupons.ts': "export interface Coupon {\n  code: string;\n  kind: 'percent' | 'amount';\n  value: number;\n  expires: string;\n}\n\nconst COUPONS: Coupon[] = [\n  { code: 'SPRING10', kind: 'percent', value: 10, expires: '2027-05-31' },\n  { code: 'WELCOME5', kind: 'amount', value: 500, expires: '2027-12-31' },\n];\n\nexport function findCoupon(code: string, now: Date): Coupon | undefined {\n  const c = COUPONS.find((c) => c.code === code.trim().toUpperCase());\n  return c && new Date(c.expires) >= now ? c : undefined;\n}\n",
    },
    memory: '# acme-shop\n\nThe Acme storefront: TypeScript, Vite, Vitest.\n\n## Conventions\n\n- Money is integer cents everywhere; format only in the view.\n- Every change to checkout comes with a test.\n- Run `npm test` and `npm run typecheck` before saying done.\n\n## Where things are\n\n- `src/checkout.ts`: the order summary\n- `src/shipping/`: rates and zones\n- `src/api/`: calls to the backend\n',
    pages: [
      { name: 'payments', title: 'Payments', description: 'How checkout talks to the payment provider', content: '# Payments\n\nThe sandbox keys are in the vault as "Payments sandbox". Never put a key in the code.\n\n## Refunds\n\nOnly from the admin panel, never from the storefront.\n' },
      { name: 'release', title: 'Releases', description: 'How a release goes out', content: '# Releases\n\nTag `vX.Y.Z` on main. CI builds and deploys to staging, then production after a manual approval.\n' },
    ],
  },
  {
    slug: 'docs-site',
    name: 'docs-site',
    art: { colors: ['#0f2a4a', '#0e7490', '#38bdf8'], motif: 'pages', seed: 23 },
    files: { 'README.md': '# docs-site\n\nThe help center for Acme customers.\n', 'content/shipping.md': '# Shipping\n\nFree from $50.\n' },
    commits: [{ author: 'Lee Park', message: 'docs: shipping page' }],
    memory: '# docs-site\n\nThe help center. Markdown in `content/`, one page per topic.\n\n## Style\n\n- Short sentences, second person.\n- Prices as on the storefront.\n',
    pages: [{ name: 'style', title: 'Writing style', description: 'How the help pages are written', content: '# Writing style\n\nSay what to do first, then why.\n' }],
  },
  {
    slug: 'mobile-app',
    name: 'mobile-app',
    art: { colors: ['#1e1b4b', '#6d28d9', '#a78bfa'], motif: 'phone', seed: 37 },
    files: { 'README.md': '# mobile-app\n\nThe Acme app for iOS and Android.\n' },
    commits: [{ author: 'Sam Okafor', message: 'chore: start the app' }],
    memory: '# mobile-app\n\nReact Native. Shares the API client with acme-shop.\n\n## Notes\n\n- Test on a small phone first.\n',
  },
  {
    slug: 'design-system',
    name: 'design-system',
    art: { colors: ['#4a1d3f', '#be185d', '#f472b6'], motif: 'shapes', seed: 41 },
    files: { 'README.md': '# design-system\n\nColours, type and components shared by the Acme storefront and app.\n', 'tokens/color.json': '{ "accent": "#c2410c" }\n' },
    commits: [{ author: 'Rina Hart', message: 'feat: colour tokens' }],
  },
  {
    slug: 'analytics',
    name: 'analytics',
    art: { colors: ['#052e2b', '#047857', '#34d399'], motif: 'bars', seed: 53 },
    files: { 'README.md': '# analytics\n\nDaily sales and funnel reports for Acme.\n', 'reports/daily.sql': 'select date(placed_at), count(*) from orders group by 1;\n' },
    commits: [{ author: 'Maya Chen', message: 'feat: daily sales report' }],
  },
  {
    slug: 'infra',
    name: 'infra',
    art: { colors: ['#111827', '#334155', '#64748b'], motif: 'nodes', seed: 67 },
    files: { 'README.md': '# infra\n\nServers, deploys and backups of the Acme shop.\n', 'deploy/staging.yml': 'hosts: [api-server, worker-1]\n' },
    commits: [{ author: 'Lee Park', message: 'chore: staging deploy' }],
  },
  // archived: the Archived view
  {
    slug: 'spring-sale',
    name: 'spring-sale',
    art: { colors: ['#3f2a0b', '#a16207', '#facc15'], motif: 'spark', seed: 71 },
    files: { 'README.md': '# spring-sale\n\nThe spring sale landing page, 2026.\n' },
    commits: [{ author: 'Sam Okafor', message: 'feat: spring sale page' }],
    archived: true,
  },
  {
    slug: 'holiday-promo',
    name: 'holiday-promo',
    art: { colors: ['#3b0d1a', '#be123c', '#fb7185'], motif: 'gift', seed: 79 },
    files: { 'README.md': '# holiday-promo\n\nGift guides and the holiday banner, 2025.\n' },
    commits: [{ author: 'Rina Hart', message: 'feat: gift guide' }],
    archived: true,
  },
  {
    slug: 'landing-v1',
    name: 'landing-v1',
    art: { colors: ['#1f2937', '#475569', '#94a3b8'], motif: 'window', seed: 83 },
    files: { 'README.md': '# landing-v1\n\nThe first Acme landing page, before the storefront.\n' },
    commits: [{ author: 'Lee Park', message: 'feat: first landing page' }],
    archived: true,
  },
];

const apiCoupons = `package coupons

import (
	"strings"
	"time"
)

// Coupon is a code a customer types at checkout.
type Coupon struct {
	Code    string
	Percent int // 10 = 10% off
	Expires time.Time
}

var known = []Coupon{
	{Code: "SPRING10", Percent: 10, Expires: time.Date(2027, 5, 31, 0, 0, 0, 0, time.UTC)},
}

// Find returns the coupon of a code, unless it is unknown or expired.
func Find(code string, now time.Time) (Coupon, bool) {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, c := range known {
		if c.Code == code && now.Before(c.Expires) {
			return c, true
		}
	}
	return Coupon{}, false
}
`;

/** acme-shop's backend and worker, each a part on a server of its own (setup.ts). */
export const PARTS: DemoPart[] = [
  {
    project: 'acme-shop',
    name: 'API',
    machine: 'api-server',
    slug: 'acme-api',
    files: {
      'README.md': '# acme-api\n\nOrders, carts and coupons for the Acme storefront.\n\n```bash\ngo run ./cmd/api   # :8080\ngo test ./...\n```\n',
      'go.mod': 'module example.com/acme/api\n\ngo 1.26\n',
      'cmd/api/main.go': 'package main\n\nimport (\n\t"log"\n\t"net/http"\n\n\t"example.com/acme/api/orders"\n)\n\nfunc main() {\n\thttp.Handle("/orders/", orders.Handler())\n\tlog.Fatal(http.ListenAndServe(":8080", nil))\n}\n',
    },
    commits: [
      { author: 'Maya Chen', message: 'feat: orders endpoint', files: { 'orders/orders.go': 'package orders\n\nimport "net/http"\n\n// Handler serves a customer\'s orders, newest first.\nfunc Handler() http.Handler {\n\treturn http.HandlerFunc(list)\n}\n\nfunc list(w http.ResponseWriter, r *http.Request) {\n\tw.Header().Set("Content-Type", "application/json")\n\t_, _ = w.Write([]byte(`[]`))\n}\n' } },
      { author: 'Sam Okafor', message: 'feat: paginate orders', files: { 'orders/page.go': 'package orders\n\n// PageSize is how many orders one page holds.\nconst PageSize = 20\n' } },
      { author: 'Maya Chen', message: 'feat: validate coupon codes', files: { 'coupons/coupons.go': apiCoupons } },
    ],
    unpushed: 1,
  },
  {
    project: 'acme-shop',
    name: 'Worker',
    machine: 'worker-1',
    slug: 'acme-worker',
    files: {
      'README.md': '# acme-worker\n\nBackground jobs: receipts, stock sync, abandoned carts.\n',
      'package.json': JSON.stringify({ name: 'acme-worker', private: true, type: 'module', scripts: { start: 'node src/index.js', test: 'vitest run' } }, null, 2) + '\n',
    },
    commits: [
      { author: 'Lee Park', message: 'feat: send receipts', files: { 'src/jobs/receipts.js': "export async function sendReceipt(order) {\n  // queued by the API when an order is paid\n  return { to: order.email, template: 'receipt', order: order.id };\n}\n" } },
      { author: 'Maya Chen', message: 'feat: nightly stock sync', files: { 'src/jobs/stock.js': "export const schedule = '0 3 * * *';\n" } },
      { author: 'Lee Park', message: 'feat: the coupon on the receipt', files: { 'src/jobs/receipts.js': "export async function sendReceipt(order) {\n  // queued by the API when an order is paid; the coupon, if any, is printed on it\n  return { to: order.email, template: 'receipt', order: order.id, coupon: order.coupon ?? null };\n}\n" } },
    ],
  },
];
