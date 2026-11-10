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
  id?: string;
  wid?: string;
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
    files: { 'README.md': '# docs-site\n\nThe help center for Acme customers.\n', 'content/shipping.md': '# Shipping\n\nFree from $50.\n' },
    commits: [{ author: 'Lee Park', message: 'docs: shipping page' }],
    memory: '# docs-site\n\nThe help center. Markdown in `content/`, one page per topic.\n\n## Style\n\n- Short sentences, second person.\n- Prices as on the storefront.\n',
    pages: [{ name: 'style', title: 'Writing style', description: 'How the help pages are written', content: '# Writing style\n\nSay what to do first, then why.\n' }],
  },
  {
    slug: 'mobile-app',
    name: 'mobile-app',
    files: { 'README.md': '# mobile-app\n\nThe Acme app for iOS and Android.\n' },
    commits: [{ author: 'Sam Okafor', message: 'chore: start the app' }],
    memory: '# mobile-app\n\nReact Native. Shares the API client with acme-shop.\n\n## Notes\n\n- Test on a small phone first.\n',
  },
];
