// Authenticator codes (RFC 6238): an otpauth:// URI or a bare Base32 key, as Bitwarden takes them.
// Steam Guard keys too: steam://KEY (Bitwarden's way) or otpauth with encoder=steam (KeeWeb's).

export interface TotpSpec {
  secret: Uint8Array<ArrayBuffer>;
  digits: number;
  period: number;
  algorithm: 'SHA-1' | 'SHA-256' | 'SHA-512';
  /** Steam Guard: 5 characters of Steam's own alphabet. */
  steam?: boolean;
}

const STEAM_ALPHABET = '23456789BCDFGHJKMNPQRTVWXY';

const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';

function base32(text: string): Uint8Array<ArrayBuffer> | null {
  const s = text.replace(/[\s-]/g, '').replace(/=+$/, '').toUpperCase();
  if (!s) return null;
  const out = new Uint8Array(Math.floor((s.length * 5) / 8));
  let bits = 0;
  let value = 0;
  let at = 0;
  for (const c of s) {
    const v = ALPHABET.indexOf(c);
    if (v < 0) return null;
    value = (value << 5) | v;
    bits += 5;
    if (bits >= 8) {
      out[at++] = (value >>> (bits - 8)) & 0xff;
      bits -= 8;
    }
  }
  return at ? out : null;
}

/** The spec of a key, or null if this is not one we can make codes from. */
export function parseTotp(raw: string): TotpSpec | null {
  const v = raw.trim();
  if (!v) return null;
  let secretText = v;
  let digits = 6;
  let period = 30;
  let algorithm: TotpSpec['algorithm'] = 'SHA-1';
  if (/^steam:\/\//i.test(v)) {
    const secret = base32(v.slice('steam://'.length));
    return secret ? { secret, digits: 5, period: 30, algorithm: 'SHA-1', steam: true } : null;
  }
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(v)) {
    let u: URL;
    try {
      u = new URL(v);
    } catch {
      return null;
    }
    if (u.protocol !== 'otpauth:' || u.hostname.toLowerCase() !== 'totp') return null;
    secretText = u.searchParams.get('secret') ?? '';
    if ((u.searchParams.get('encoder') ?? '').toLowerCase() === 'steam') {
      const secret = base32(secretText);
      return secret ? { secret, digits: 5, period: 30, algorithm: 'SHA-1', steam: true } : null;
    }
    const d = Number(u.searchParams.get('digits') ?? 6);
    const p = Number(u.searchParams.get('period') ?? 30);
    if (!Number.isInteger(d) || d < 6 || d > 10) return null;
    if (!Number.isInteger(p) || p < 1 || p > 3600) return null;
    digits = d;
    period = p;
    const algo = (u.searchParams.get('algorithm') ?? 'SHA1').toUpperCase().replace('-', '');
    if (algo === 'SHA1') algorithm = 'SHA-1';
    else if (algo === 'SHA256') algorithm = 'SHA-256';
    else if (algo === 'SHA512') algorithm = 'SHA-512';
    else return null;
  }
  const secret = base32(secretText);
  return secret ? { secret, digits, period, algorithm } : null;
}

/** Not crypto.subtle: it is missing on plain HTTP, and a code needs no more protection than the key beside it. */
export async function totpCode(spec: TotpSpec, nowMs: number): Promise<string> {
  const [{ hmac }, { sha1 }, { sha256, sha512 }] = await Promise.all([import('@noble/hashes/hmac.js'), import('@noble/hashes/legacy.js'), import('@noble/hashes/sha2.js')]);
  const hash = spec.algorithm === 'SHA-1' ? sha1 : spec.algorithm === 'SHA-256' ? sha256 : sha512;
  const counter = Math.floor(nowMs / 1000 / spec.period);
  const msg = new Uint8Array(8);
  new DataView(msg.buffer).setBigUint64(0, BigInt(counter));
  const mac = hmac(hash, spec.secret, msg);
  const at = mac[mac.length - 1] & 0x0f;
  const bin = new DataView(mac.buffer, mac.byteOffset, mac.byteLength).getUint32(at) & 0x7fffffff;
  if (spec.steam) {
    let full = bin;
    let code = '';
    for (let i = 0; i < 5; i++) {
      code += STEAM_ALPHABET[full % STEAM_ALPHABET.length];
      full = Math.floor(full / STEAM_ALPHABET.length);
    }
    return code;
  }
  return String(bin % 10 ** spec.digits).padStart(spec.digits, '0');
}

/** "123 456": easier to read across from a screen. A Steam code stays as Steam shows it. */
export function formatCode(code: string): string {
  if (/[A-Z]/.test(code)) return code;
  const mid = Math.ceil(code.length / 2);
  return `${code.slice(0, mid)} ${code.slice(mid)}`;
}
