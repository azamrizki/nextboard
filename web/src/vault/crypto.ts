// The vault's encryption (docs/API-vault.md). All of it happens here, in the browser: the hub gets
// the wrapped key and the blobs and never the master password, so nothing on the hub's side,
// agents included, can read a credential without guessing that password.
//
//   master password --Argon2id--> key-encryption key --AES-256-GCM--> vault key --AES-256-GCM--> items
//
// The vault key is random and never changes, which is why a new master password only has to wrap
// it again. Once open it lives in memory only: a non-extractable CryptoKey on HTTPS and localhost,
// plain bytes on plain HTTP, where browsers hide crypto.subtle. Both make the same AES-GCM bytes,
// so one vault opens either way.

export interface KdfParams {
  name: 'argon2id';
  /** Memory, KiB. */
  m: number;
  t: number;
  p: number;
}

export interface KeyMaterial {
  kdf: KdfParams;
  salt: string;
  wrappedKey: string;
}

/** The same cost Bitwarden uses by default for Argon2id. */
export const DEFAULT_KDF: KdfParams = { name: 'argon2id', m: 64 * 1024, t: 3, p: 4 };

const NONCE = 12;
const KEY_AAD = new TextEncoder().encode('nextboard-vault/v1/key');
/** A blob only opens under its own id, so the hub cannot show one item as another. */
const itemAad = (id: string) => new TextEncoder().encode(`nextboard-vault/v1/item/${id}`);
/** Plaintext is padded to this, so that a blob's size says little about what is in it. */
const PAD_TO = 512;
/** The hub's limit for one item, as base64. */
export const MAX_BLOB_CHARS = 64 << 10;

export class WrongPassword extends Error {
  constructor() {
    super('Wrong master password.');
    this.name = 'WrongPassword';
  }
}

/** What the vault cannot do without. All of it exists on plain HTTP too. */
export function vaultSupported(): boolean {
  return typeof globalThis.crypto?.getRandomValues === 'function' && typeof Worker !== 'undefined' && typeof WebAssembly !== 'undefined';
}

/** The hub is local-first and often reached like this. It works, and the card says what it means. */
export const plainHttp = () => !window.isSecureContext;

// ---- AES-256-GCM ----

/** A key that can be used and not read back through this interface. */
export interface AeadKey {
  seal(iv: Uint8Array<ArrayBuffer>, plain: Uint8Array<ArrayBuffer>, aad: Uint8Array<ArrayBuffer>): Promise<Uint8Array>;
  open(iv: Uint8Array<ArrayBuffer>, sealed: Uint8Array<ArrayBuffer>, aad: Uint8Array<ArrayBuffer>): Promise<Uint8Array>;
  /** From here on it refuses to work, so that nothing still running encrypts with a dead key. */
  wipe(): void;
}

async function aeadKey(raw: Uint8Array<ArrayBuffer>): Promise<AeadKey> {
  let live = true;
  const check = () => {
    if (!live) throw new Error('The vault is locked.');
  };
  const subtle = globalThis.crypto.subtle as SubtleCrypto | undefined;
  if (subtle) {
    const key = await subtle.importKey('raw', raw, 'AES-GCM', false, ['encrypt', 'decrypt']);
    return {
      seal: async (iv, plain, aad) => {
        check();
        return new Uint8Array(await subtle.encrypt({ name: 'AES-GCM', iv, additionalData: aad }, key, plain));
      },
      open: async (iv, sealed, aad) => {
        check();
        return new Uint8Array(await subtle.decrypt({ name: 'AES-GCM', iv, additionalData: aad }, key, sealed));
      },
      wipe: () => {
        live = false;
      },
    };
  }
  const { gcm } = await import('@noble/ciphers/aes.js');
  const own = raw.slice();
  return {
    seal: async (iv, plain, aad) => {
      check();
      return gcm(own, iv, aad).encrypt(plain);
    },
    open: async (iv, sealed, aad) => {
      check();
      return gcm(own, iv, aad).decrypt(sealed);
    },
    wipe: () => {
      live = false;
      own.fill(0);
    },
  };
}

// ---- bytes ----

export function toB64(bytes: Uint8Array): string {
  let s = '';
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return btoa(s);
}

export function fromB64(text: string): Uint8Array<ArrayBuffer> {
  const s = atob(text);
  const out = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
  return out;
}

const random = (n: number) => crypto.getRandomValues(new Uint8Array(n));

/** A record id: nothing in it says what the record is. */
export function randomId(): string {
  return toB64(random(16)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function concat(a: Uint8Array, b: Uint8Array): Uint8Array {
  const out = new Uint8Array(a.length + b.length);
  out.set(a);
  out.set(b, a.length);
  return out;
}

// ---- key derivation ----

/** A header outside these never came from this code: refuse it rather than run it. */
function checkKdf(kdf: KdfParams) {
  const ok = kdf?.name === 'argon2id' && kdf.m >= 32 << 10 && kdf.m <= 1 << 20 && kdf.t >= 2 && kdf.t <= 16 && kdf.p >= 1 && kdf.p <= 16;
  if (!ok) throw new Error('This vault asks for encryption settings NextBoard does not accept.');
}

/** Argon2id in a worker: it takes a second on purpose, and the board should not stop for it. */
function argon2(password: string, salt: Uint8Array, kdf: KdfParams): Promise<Uint8Array<ArrayBuffer>> {
  return new Promise((resolve, reject) => {
    const worker = new Worker(new URL('./kdf.worker.ts', import.meta.url), { type: 'module' });
    worker.onmessage = (e: MessageEvent<{ hash?: Uint8Array<ArrayBuffer>; error?: string }>) => {
      worker.terminate();
      if (e.data.hash) resolve(e.data.hash);
      else reject(new Error(e.data.error ?? 'Key derivation failed.'));
    };
    worker.onerror = (e) => {
      worker.terminate();
      reject(new Error(e.message || 'Key derivation failed.'));
    };
    // The same password typed on another system must give the same key.
    const bytes = new TextEncoder().encode(password.normalize('NFKC'));
    worker.postMessage({ password: bytes, salt, m: kdf.m, t: kdf.t, p: kdf.p }, [bytes.buffer]);
  });
}

async function deriveKek(password: string, salt: Uint8Array, kdf: KdfParams): Promise<AeadKey> {
  const raw = await argon2(password, salt, kdf);
  try {
    return await aeadKey(raw);
  } finally {
    raw.fill(0);
  }
}

async function wrapRaw(raw: Uint8Array<ArrayBuffer>, password: string): Promise<KeyMaterial> {
  const kdf = DEFAULT_KDF;
  const salt = random(16);
  const kek = await deriveKek(password, salt, kdf);
  try {
    const iv = random(NONCE);
    return { kdf, salt: toB64(salt), wrappedKey: toB64(concat(iv, await kek.seal(iv, raw, KEY_AAD))) };
  } finally {
    kek.wipe();
  }
}

/** The vault key's bytes, for as short as they are needed. A wrong password fails the GCM tag. */
async function unwrapRaw(password: string, m: KeyMaterial): Promise<Uint8Array<ArrayBuffer>> {
  checkKdf(m.kdf);
  const blob = fromB64(m.wrappedKey);
  const kek = await deriveKek(password, fromB64(m.salt), m.kdf);
  try {
    return Uint8Array.from(await kek.open(blob.subarray(0, NONCE), blob.subarray(NONCE), KEY_AAD));
  } catch {
    throw new WrongPassword();
  } finally {
    kek.wipe();
  }
}

/** A new vault: a random key, wrapped under the password. */
export async function createKey(password: string): Promise<{ material: KeyMaterial; key: AeadKey }> {
  const raw = random(32);
  try {
    const material = await wrapRaw(raw, password);
    return { material, key: await aeadKey(raw) };
  } finally {
    raw.fill(0);
  }
}

export async function openKey(password: string, m: KeyMaterial): Promise<AeadKey> {
  const raw = await unwrapRaw(password, m);
  try {
    return await aeadKey(raw);
  } finally {
    raw.fill(0);
  }
}

/** The same vault key under a new password. The open key cannot be read back, so the old password is what gets to it. */
export async function rewrapKey(oldPassword: string, newPassword: string, m: KeyMaterial): Promise<KeyMaterial> {
  const raw = await unwrapRaw(oldPassword, m);
  try {
    return await wrapRaw(raw, newPassword);
  } finally {
    raw.fill(0);
  }
}

// ---- records ----

export async function seal(key: AeadKey, id: string, value: unknown): Promise<string> {
  const json = JSON.stringify(value);
  const size = new TextEncoder().encode(json).length;
  // JSON.parse ignores the trailing spaces.
  const padded = new TextEncoder().encode(json + ' '.repeat((PAD_TO - (size % PAD_TO)) % PAD_TO));
  const iv = random(NONCE);
  const blob = toB64(concat(iv, await key.seal(iv, padded, itemAad(id))));
  if (blob.length > MAX_BLOB_CHARS) throw new Error('This item is too large to save. Shorten its notes or fields.');
  return blob;
}

export async function open<T>(key: AeadKey, id: string, blob: string): Promise<T> {
  const raw = fromB64(blob);
  const plain = await key.open(raw.subarray(0, NONCE), raw.subarray(NONCE), itemAad(id));
  return JSON.parse(new TextDecoder().decode(plain)) as T;
}

// ---- PIN ----
// Six digits cannot protect a key alone, so a PIN needs this browser's record and the hub's say:
// the secret that wraps the key comes from the hub, for the right proof, and only so many wrong
// tries (internal/vault/pin.go). The record below holds no PIN and is useless without the hub.

const PIN_AAD = new TextEncoder().encode('nextboard-vault/v1/pin');

export interface PinRecord {
  vaultId: string;
  pinId: string;
  kdf: KdfParams;
  salt: string;
  wrappedKey: string;
}

async function pinProof(pin: string, salt: Uint8Array, kdf: KdfParams): Promise<string> {
  const raw = await argon2(pin, salt, kdf);
  try {
    return toB64(raw);
  } finally {
    raw.fill(0);
  }
}

/** Set a PIN up. The open key cannot be read back, so the master password is what gets to it. */
export async function wrapKeyForPin(master: string, pin: string, m: KeyMaterial, register: (proof: string) => Promise<{ id: string; secret: string }>): Promise<Omit<PinRecord, 'vaultId'>> {
  const raw = await unwrapRaw(master, m);
  try {
    const kdf = DEFAULT_KDF;
    const salt = random(16);
    const { id, secret } = await register(await pinProof(pin, salt, kdf));
    const k = await aeadKey(fromB64(secret));
    try {
      const iv = random(NONCE);
      return { pinId: id, kdf, salt: toB64(salt), wrappedKey: toB64(concat(iv, await k.seal(iv, raw, PIN_AAD))) };
    } finally {
      k.wipe();
    }
  } finally {
    raw.fill(0);
  }
}

export function proofForPin(pin: string, rec: PinRecord): Promise<string> {
  checkKdf(rec.kdf);
  return pinProof(pin, fromB64(rec.salt), rec.kdf);
}

export async function keyFromPinSecret(secret: string, rec: PinRecord): Promise<AeadKey> {
  const k = await aeadKey(fromB64(secret));
  const blob = fromB64(rec.wrappedKey);
  try {
    const raw = Uint8Array.from(await k.open(blob.subarray(0, NONCE), blob.subarray(NONCE), PIN_AAD));
    try {
      return await aeadKey(raw);
    } finally {
      raw.fill(0);
    }
  } finally {
    k.wipe();
  }
}
