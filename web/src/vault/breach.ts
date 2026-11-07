// Has a password shown up in a data breach? Asked of Have I Been Pwned's Pwned Passwords range
// API by k-anonymity: only the first 5 hex characters of the password's SHA-1 leave this browser,
// and the answer is every suffix sharing them, compared here. Padded answers hide even how many
// there are. The hub passes the prefix on (GET /api/vault/pwned/{prefix}), so that this page
// connects to nothing but the hub. Never automatic: a person asks, item by item or all at once.

const RANGE_URL = '/api/vault/pwned/';

/** What leaves the browser. */
export const BREACH_NOTE = 'Only the first 5 characters of a hash of the password go out, through the hub to Have I Been Pwned. The password never leaves this browser.';

async function sha1Hex(text: string): Promise<string> {
  // not crypto.subtle: it is missing on plain HTTP
  const { sha1 } = await import('@noble/hashes/legacy.js');
  const d = sha1(new TextEncoder().encode(text));
  return Array.from(d, (b) => b.toString(16).padStart(2, '0'))
    .join('')
    .toUpperCase();
}

type Fetch = (url: string, init: RequestInit) => Promise<Response>;

/** How many times the password was seen in breaches; 0 for none. */
export async function pwnedCount(password: string, get: Fetch = fetch, cache?: Map<string, string>): Promise<number> {
  const hash = await sha1Hex(password);
  const prefix = hash.slice(0, 5);
  const suffix = hash.slice(5);
  let body = cache?.get(prefix);
  if (body === undefined) {
    const r = await get(RANGE_URL + prefix, { credentials: 'same-origin' });
    if (!r.ok) {
      let msg = `the hub answered ${r.status}`;
      try {
        msg = ((await r.json()) as { error?: string }).error || msg;
      } catch {
        /* not JSON */
      }
      throw new Error(msg);
    }
    body = await r.text();
    cache?.set(prefix, body);
  }
  for (const line of body.split('\n')) {
    const [s, n] = line.trim().split(':');
    // padding lines carry a count of 0
    if (s === suffix) return Number(n) || 0;
  }
  return 0;
}
