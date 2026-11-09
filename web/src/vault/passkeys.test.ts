import { createPublicKey, createVerify, createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { createPasskey, fromB64url, signWithPasskey, type PasskeyRequest } from './passkeys';

// A relying party's own check, by the book (WebAuthn §7.1, §7.2), with Node's crypto and not the
// library that signed: if this holds, a server would accept what the vault produces.

const b = (s: string) => Buffer.from(fromB64url(s));
const sha = (d: Uint8Array) => createHash('sha256').update(d).digest();

const creating: PasskeyRequest = {
  id: 'pk_1',
  kind: 'create',
  origin: 'https://www.example.com',
  rpId: 'example.com',
  createdAt: '',
  options: { rp: { id: 'example.com', name: 'Example' }, user: { id: 'dXNlcg', name: 'alice' }, challenge: 'Y2hhbGxlbmdl' },
};

/** Reads just enough CBOR for the attestation object and the COSE key. */
function cbor(buf: Buffer, at = 0): [unknown, number] {
  const head = buf[at++];
  const major = head >> 5;
  let n = head & 31;
  if (n === 24) n = buf[at++];
  else if (n === 25) {
    n = buf.readUInt16BE(at);
    at += 2;
  }
  if (major === 0) return [n, at];
  if (major === 1) return [-1 - n, at];
  if (major === 2) return [buf.subarray(at, at + n), at + n];
  if (major === 3) return [buf.subarray(at, at + n).toString(), at + n];
  if (major === 5) {
    const m = new Map<unknown, unknown>();
    for (let i = 0; i < n; i++) {
      const [k, a] = cbor(buf, at);
      const [v, c] = cbor(buf, a);
      m.set(k, v);
      at = c;
    }
    return [m, at];
  }
  throw new Error(`major ${major}`);
}

describe('a passkey in the vault', () => {
  it('registers as a relying party checks it', () => {
    const { result, stored } = createPasskey(creating);
    const cd = JSON.parse(b(result.clientDataJSON).toString());
    expect(cd).toEqual({ type: 'webauthn.create', challenge: 'Y2hhbGxlbmdl', origin: 'https://www.example.com', crossOrigin: false });

    const [att] = cbor(b(result.attestationObject)) as [Map<string, unknown>, number];
    expect(att.get('fmt')).toBe('none');
    expect((att.get('attStmt') as Map<unknown, unknown>).size).toBe(0);
    const auth = att.get('authData') as Buffer;
    expect(Buffer.compare(auth, b(result.authenticatorData))).toBe(0);

    expect(auth.subarray(0, 32).equals(sha(Buffer.from('example.com')))).toBe(true);
    const flags = auth[32];
    expect(flags & 0x01).toBe(0x01); // user present
    expect(flags & 0x04).toBe(0x04); // user verified
    expect(flags & 0x40).toBe(0x40); // attested credential
    expect(auth.readUInt32BE(33)).toBe(0);
    const idLen = auth.readUInt16BE(53);
    expect(auth.subarray(55, 55 + idLen).equals(b(result.id))).toBe(true);

    const [cose] = cbor(auth, 55 + idLen) as [Map<number, unknown>, number];
    expect(cose.get(1)).toBe(2);
    expect(cose.get(3)).toBe(-7);
    expect(cose.get(-1)).toBe(1);
    const x = cose.get(-2) as Buffer;
    const y = cose.get(-3) as Buffer;
    expect(x.length).toBe(32);
    expect(y.length).toBe(32);

    // the SPKI handed to the page is that same key
    const spki = createPublicKey({ key: b(result.publicKey), format: 'der', type: 'spki' });
    const jwk = spki.export({ format: 'jwk' });
    expect(jwk.crv).toBe('P-256');
    expect(Buffer.from(jwk.x!, 'base64url').equals(x)).toBe(true);
    expect(Buffer.from(jwk.y!, 'base64url').equals(y)).toBe(true);

    expect(stored).toMatchObject({ rpId: 'example.com', userName: 'alice', userHandle: 'dXNlcg', credentialId: result.id });
    expect(stored.secret).toBeTruthy();
    expect(JSON.stringify(result)).not.toContain(stored.secret);
  });

  it('signs in as a relying party checks it', () => {
    const { result, stored } = createPasskey(creating);
    const asking: PasskeyRequest = { id: 'pk_2', kind: 'get', origin: 'https://example.com', rpId: 'example.com', createdAt: '', options: { rpId: 'example.com', challenge: 'c2lnbi1pbg' } };
    const r = signWithPasskey(asking, { ...stored, createdAt: '' });
    const cd = JSON.parse(b(r.clientDataJSON).toString());
    expect(cd.type).toBe('webauthn.get');
    expect(cd.challenge).toBe('c2lnbi1pbg');
    expect(cd.origin).toBe('https://example.com');
    const auth = b(r.authenticatorData);
    expect(auth.length).toBe(37);
    expect(auth.subarray(0, 32).equals(sha(Buffer.from('example.com')))).toBe(true);
    expect(auth[32] & 0x05).toBe(0x05);
    expect(r.userHandle).toBe('dXNlcg');

    const key = createPublicKey({ key: b(result.publicKey), format: 'der', type: 'spki' });
    const good = createVerify('SHA256').update(auth).update(sha(b(r.clientDataJSON))).verify(key, b(r.signature));
    expect(good).toBe(true);
    // and a signature is bound to what it signed
    const other = createVerify('SHA256').update(auth).update(sha(Buffer.from('another challenge'))).verify(key, b(r.signature));
    expect(other).toBe(false);
  });

  it('makes a new key every time', () => {
    const a = createPasskey(creating);
    const c = createPasskey(creating);
    expect(a.stored.secret).not.toBe(c.stored.secret);
    expect(a.result.id).not.toBe(c.result.id);
  });
});
