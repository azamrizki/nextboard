import { createDecipheriv, createHash } from 'node:crypto';
import * as nodeCrypto from 'node:crypto';
import { argon2id } from 'hash-wasm';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { createKey, DEFAULT_KDF, fromB64, keyFromPinSecret, MAX_BLOB_CHARS, open, openKey, proofForPin, rewrapKey, seal, toB64, WrongPassword, wrapKeyForPin, type KeyMaterial, type PinRecord } from './crypto';

// The real worker's code, on this thread: Node has no Worker of the browser's kind.
const scope: { onmessage?: (e: { data: unknown }) => Promise<void>; postMessage?: (m: unknown) => void } = {};
class WorkerOnThisThread {
  onmessage: ((e: { data: unknown }) => void) | null = null;
  onerror: ((e: unknown) => void) | null = null;
  postMessage(data: unknown) {
    scope.postMessage = (m) => this.onmessage?.({ data: m });
    void scope.onmessage?.({ data });
  }
  terminate() {}
}

let material: KeyMaterial;
let key: Awaited<ReturnType<typeof createKey>>['key'];
const PASSWORD = 'correct horse battery staple';

beforeAll(async () => {
  vi.stubGlobal('self', scope);
  vi.stubGlobal('Worker', WorkerOnThisThread);
  await import('./kdf.worker');
  ({ material, key } = await createKey(PASSWORD));
});

afterEach(() => {
  vi.stubGlobal('crypto', nodeCrypto.webcrypto);
});

/** What a browser on plain HTTP has: random numbers, and no crypto.subtle. */
function plainHttp() {
  const real = nodeCrypto.webcrypto;
  vi.stubGlobal('crypto', { getRandomValues: <T extends ArrayBufferView>(a: T) => real.getRandomValues(a as never) });
}

function flip(b64: string, at: number) {
  const raw = fromB64(b64);
  raw[at] ^= 1;
  return toB64(raw);
}

describe('the master password', () => {
  it('opens what was sealed', async () => {
    const blob = await seal(key, 'id1', { secret: 'hunter2' });
    const again = await openKey(PASSWORD, material);
    await expect(open(again, 'id1', blob)).resolves.toEqual({ secret: 'hunter2' });
  });

  it('is told apart from a wrong one', async () => {
    await expect(openKey('not the password', material)).rejects.toBeInstanceOf(WrongPassword);
  });

  it('is the same however its characters are composed', async () => {
    const { material: m } = await createKey('café au lait');
    await expect(openKey('café au lait', m)).resolves.toBeTruthy();
  });

  it('never reaches the hub, not even as part of what it stores', () => {
    const stored = JSON.stringify(material);
    expect(stored).not.toContain(PASSWORD);
    expect(stored).not.toContain(toB64(new TextEncoder().encode(PASSWORD)));
    expect(fromB64(material.salt)).toHaveLength(16);
    // nonce + vault key + GCM tag
    expect(fromB64(material.wrappedKey)).toHaveLength(12 + 32 + 16);
  });

  it('gets a new salt and a new wrapped key each time', async () => {
    const { material: other } = await createKey(PASSWORD);
    expect(other.salt).not.toBe(material.salt);
    expect(other.wrappedKey).not.toBe(material.wrappedKey);
  });

  it('can change without touching the items', async () => {
    const blob = await seal(key, 'id1', 'kept');
    const moved = await rewrapKey(PASSWORD, 'a new password', material);
    await expect(open(await openKey('a new password', moved), 'id1', blob)).resolves.toBe('kept');
    await expect(openKey(PASSWORD, moved)).rejects.toBeInstanceOf(WrongPassword);
    await expect(rewrapKey('wrong', 'x y z', material)).rejects.toBeInstanceOf(WrongPassword);
  });
});

describe('a header from the hub', () => {
  it.each([
    ['too little memory', { ...DEFAULT_KDF, m: 1024 }],
    ['too much memory', { ...DEFAULT_KDF, m: 1 << 30 }],
    ['one pass', { ...DEFAULT_KDF, t: 1 }],
    ['no threads', { ...DEFAULT_KDF, p: 0 }],
    ['another function', { ...DEFAULT_KDF, name: 'pbkdf2' }],
  ])('cannot weaken the key derivation: %s', async (_name, kdf) => {
    const run = vi.spyOn(scope, 'onmessage');
    await expect(openKey(PASSWORD, { ...material, kdf: kdf as KeyMaterial['kdf'] })).rejects.toThrow('does not accept');
    expect(run).not.toHaveBeenCalled();
    run.mockRestore();
  });
});

describe('an item', () => {
  it('only opens under its own id', async () => {
    const blob = await seal(key, 'id1', 'mine');
    await expect(open(key, 'id2', blob)).rejects.toThrow();
  });

  it('refuses any changed byte', async () => {
    const blob = await seal(key, 'id1', 'mine');
    for (const at of [0, 11, 12, 100, fromB64(blob).length - 1]) await expect(open(key, 'id1', flip(blob, at))).rejects.toThrow();
  });

  it('does not open under another vault', async () => {
    const { key: other } = await createKey('another vault');
    await expect(open(other, 'id1', await seal(key, 'id1', 'mine'))).rejects.toThrow();
  });

  it('uses a new nonce each time', async () => {
    expect(await seal(key, 'id1', 'same')).not.toBe(await seal(key, 'id1', 'same'));
  });

  it('hides its size in steps of 512 bytes', async () => {
    const size = async (v: unknown) => fromB64(await seal(key, 'id1', v)).length;
    expect(await size('a')).toBe(12 + 512 + 16);
    expect(await size('a much longer secret than the other one')).toBe(12 + 512 + 16);
    expect(await size('x'.repeat(600))).toBe(12 + 1024 + 16);
    await expect(open(key, 'id1', await seal(key, 'id1', { a: 'é'.repeat(300) }))).resolves.toEqual({ a: 'é'.repeat(300) });
  });

  it('that is too large is refused, not cut', async () => {
    await expect(seal(key, 'id1', 'x'.repeat(MAX_BLOB_CHARS))).rejects.toThrow('too large');
  });
});

describe('a locked vault', () => {
  it('refuses to work', async () => {
    const k = await openKey(PASSWORD, material);
    const blob = await seal(k, 'id1', 'x');
    k.wipe();
    await expect(seal(k, 'id1', 'y')).rejects.toThrow('locked');
    await expect(open(k, 'id1', blob)).rejects.toThrow('locked');
  });

  it('refuses to work on plain HTTP too', async () => {
    plainHttp();
    const k = await openKey(PASSWORD, material);
    k.wipe();
    await expect(seal(k, 'id1', 'y')).rejects.toThrow('locked');
  });
});

// The hub is often reached over http://, where browsers hide crypto.subtle.
describe('on plain HTTP', () => {
  it('one vault opens either way', async () => {
    const fromHttps = await seal(key, 'id1', 'sealed on https');
    plainHttp();
    expect(globalThis.crypto.subtle).toBeUndefined();
    const k = await openKey(PASSWORD, material);
    await expect(open(k, 'id1', fromHttps)).resolves.toBe('sealed on https');
    const fromHttp = await seal(k, 'id2', 'sealed on http');
    vi.stubGlobal('crypto', nodeCrypto.webcrypto);
    await expect(open(key, 'id2', fromHttp)).resolves.toBe('sealed on http');
  });

  it('a new vault made there opens on https', async () => {
    plainHttp();
    const { material: m, key: k } = await createKey('made on http');
    const blob = await seal(k, 'id1', 'x');
    vi.stubGlobal('crypto', nodeCrypto.webcrypto);
    await expect(open(await openKey('made on http', m), 'id1', blob)).resolves.toBe('x');
  });

  it('a wrong password is still told apart', async () => {
    plainHttp();
    await expect(openKey('nope', material)).rejects.toBeInstanceOf(WrongPassword);
  });
});

// docs/API-vault.md promises a vault can be opened without NextBoard's code.
describe('the format', () => {
  async function openByHand(password: string, m: KeyMaterial, id: string, blob: string) {
    const kek = await argon2id({ password: password.normalize('NFKC'), salt: fromB64(m.salt), memorySize: m.kdf.m, iterations: m.kdf.t, parallelism: m.kdf.p, hashLength: 32, outputType: 'binary' });
    const unseal = (k: Uint8Array, raw: Uint8Array, aad: string) => {
      const d = createDecipheriv('aes-256-gcm', k, raw.subarray(0, 12));
      d.setAuthTag(raw.subarray(raw.length - 16));
      d.setAAD(new TextEncoder().encode(aad));
      return Buffer.concat([d.update(raw.subarray(12, raw.length - 16)), d.final()]);
    };
    const vaultKey = unseal(kek, fromB64(m.wrappedKey), 'nextboard-vault/v1/key');
    return JSON.parse(unseal(vaultKey, fromB64(blob), `nextboard-vault/v1/item/${id}`).toString());
  }

  it('opens with a standard Argon2id and AES-GCM', async () => {
    await expect(openByHand(PASSWORD, material, 'id1', await seal(key, 'id1', { secret: 'by hand' }))).resolves.toEqual({ secret: 'by hand' });
  });

  it('derives what another Argon2id derives', async () => {
    const other = (nodeCrypto as unknown as { argon2Sync?: (a: string, o: object) => Uint8Array }).argon2Sync;
    if (!other) return; // Node 24 and up
    const salt = fromB64(material.salt);
    const ours = await argon2id({ password: PASSWORD, salt, memorySize: 65536, iterations: 3, parallelism: 4, hashLength: 32, outputType: 'binary' });
    const theirs = other('argon2id', { message: PASSWORD, nonce: salt, memory: 65536, passes: 3, parallelism: 4, tagLength: 32 });
    expect(createHash('sha256').update(ours).digest('hex')).toBe(createHash('sha256').update(theirs).digest('hex'));
  });
});

describe('a PIN', () => {
  const secret = toB64(nodeCrypto.randomBytes(32));

  async function setUp(pin: string) {
    let proof = '';
    const rec = await wrapKeyForPin(PASSWORD, pin, material, async (p) => {
      proof = p;
      return { id: 'pin1', secret };
    });
    return { rec: { ...rec, vaultId: 'v1' } as PinRecord, proof };
  }

  it('opens the vault with the secret the hub gives for the right proof', async () => {
    const blob = await seal(key, 'id1', 'behind a pin');
    const { rec, proof } = await setUp('123456');
    await expect(proofForPin('123456', rec)).resolves.toBe(proof);
    await expect(open(await keyFromPinSecret(secret, rec), 'id1', blob)).resolves.toBe('behind a pin');
  });

  it('gives another proof for another PIN', async () => {
    const { rec, proof } = await setUp('123456');
    await expect(proofForPin('654321', rec)).resolves.not.toBe(proof);
  });

  it('stays useless without the hub: the record holds neither PIN nor key', async () => {
    const { rec } = await setUp('123456');
    expect(JSON.stringify(rec)).not.toContain('123456');
    await expect(keyFromPinSecret(toB64(nodeCrypto.randomBytes(32)), rec)).rejects.toThrow();
  });

  it('needs the master password to set up', async () => {
    await expect(wrapKeyForPin('wrong', '123456', material, async () => ({ id: 'x', secret }))).rejects.toBeInstanceOf(WrongPassword);
  });

  it('cannot be weakened by a stored header', async () => {
    const { rec } = await setUp('123456');
    expect(() => proofForPin('123456', { ...rec, kdf: { ...DEFAULT_KDF, m: 1024 } })).toThrow('does not accept');
  });
});
