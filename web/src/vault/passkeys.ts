// Passkeys of other sites, in the vault (docs/API-vault.md). The private key lives in the item's
// blob and signs here, in the owner's browser: the hub and the extension only carry the result.
// @noble/curves and not WebCrypto, because a hub on plain HTTP has no crypto.subtle.

import { p256 } from '@noble/curves/nist.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { apiRequest } from '../api';
import type { StoredPasskey } from './model';

export interface PasskeyAskCreate {
  rp: { id?: string; name?: string };
  user: { id: string; name: string; displayName?: string };
  challenge: string;
  excludeCredentials?: { id: string }[];
}

export interface PasskeyAskGet {
  rpId?: string;
  challenge: string;
  allowCredentials?: { id: string }[];
}

export interface PasskeyRequest {
  id: string;
  kind: 'create' | 'get';
  /** Said by the browser, not by the page. */
  origin: string;
  /** Checked by the hub against the origin. */
  rpId: string;
  options: PasskeyAskCreate | PasskeyAskGet;
  createdAt: string;
}

export interface PasskeyStatus {
  enabled: boolean;
  active: boolean;
  path?: string;
  requests: PasskeyRequest[] | null;
}

export const passkeyApi = {
  status: () => apiRequest<PasskeyStatus>('GET', '/api/vault/passkeys'),
  setEnabled: (enabled: boolean) => apiRequest<{ enabled: boolean; path: string }>('PUT', '/api/vault/passkeys', { enabled }),
  answer: (id: string, body: { result: unknown } | { fallback: true } | { refuse: true }) => apiRequest<unknown>('POST', `/api/vault/passkeys/requests/${encodeURIComponent(id)}`, body),
};

export const toB64url = (b: Uint8Array) => {
  let s = '';
  for (const x of b) s += String.fromCharCode(x);
  return btoa(s).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
};

export const fromB64url = (s: string) => {
  const t = atob(s.replaceAll('-', '+').replaceAll('_', '/'));
  const b = new Uint8Array(t.length);
  for (let i = 0; i < t.length; i++) b[i] = t.charCodeAt(i);
  return b;
};

const concat = (...parts: Uint8Array[]) => {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let at = 0;
  for (const p of parts) {
    out.set(p, at);
    at += p.length;
  }
  return out;
};

const text = (s: string) => new TextEncoder().encode(s);

// ---- the little CBOR an attestation needs ----

function cborHead(major: number, n: number): Uint8Array {
  if (n < 24) return Uint8Array.of((major << 5) | n);
  if (n < 256) return Uint8Array.of((major << 5) | 24, n);
  return Uint8Array.of((major << 5) | 25, n >> 8, n & 255);
}
const cborInt = (n: number) => (n >= 0 ? cborHead(0, n) : cborHead(1, -1 - n));
const cborBytes = (b: Uint8Array) => concat(cborHead(2, b.length), b);
const cborText = (s: string) => {
  const b = text(s);
  return concat(cborHead(3, b.length), b);
};
const cborMap = (entries: [Uint8Array, Uint8Array][]) => concat(cborHead(5, entries.length), ...entries.flat());

/** Flags: user present, user verified (the vault is open and a person approved), backed up. */
const FLAG_UP = 0x01;
const FLAG_UV = 0x04;
const FLAG_BE = 0x08;
const FLAG_BS = 0x10;
const FLAG_AT = 0x40;
const BASE_FLAGS = FLAG_UP | FLAG_UV | FLAG_BE | FLAG_BS;

const SPKI_P256 = Uint8Array.of(0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01, 0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00);

function clientData(type: string, challenge: string, origin: string) {
  return text(JSON.stringify({ type, challenge, origin, crossOrigin: false }));
}

export interface CreatedPasskey {
  stored: Omit<StoredPasskey, 'createdAt'>;
  /** What the page receives. */
  result: {
    id: string;
    clientDataJSON: string;
    attestationObject: string;
    authenticatorData: string;
    publicKey: string;
    publicKeyAlgorithm: number;
  };
}

/** A new key for a site. Nothing here is saved or sent yet. */
export function createPasskey(req: PasskeyRequest): CreatedPasskey {
  const o = req.options as PasskeyAskCreate;
  const secret = p256.utils.randomSecretKey();
  const point = p256.getPublicKey(secret, false); // 04 | x | y
  const credId = crypto.getRandomValues(new Uint8Array(32));
  const cose = cborMap([
    [cborInt(1), cborInt(2)], // EC2
    [cborInt(3), cborInt(-7)], // ES256
    [cborInt(-1), cborInt(1)], // P-256
    [cborInt(-2), cborBytes(point.slice(1, 33))],
    [cborInt(-3), cborBytes(point.slice(33, 65))],
  ]);
  const authData = concat(sha256(text(req.rpId)), Uint8Array.of(BASE_FLAGS | FLAG_AT), new Uint8Array(4), new Uint8Array(16), Uint8Array.of(credId.length >> 8, credId.length & 255), credId, cose);
  const attestation = cborMap([
    [cborText('fmt'), cborText('none')],
    [cborText('attStmt'), cborMap([])],
    [cborText('authData'), cborBytes(authData)],
  ]);
  const id = toB64url(credId);
  return {
    stored: { rpId: req.rpId, credentialId: id, userHandle: o.user.id, userName: o.user.name, secret: toB64url(secret) },
    result: {
      id,
      clientDataJSON: toB64url(clientData('webauthn.create', o.challenge, req.origin)),
      attestationObject: toB64url(attestation),
      authenticatorData: toB64url(authData),
      publicKey: toB64url(concat(SPKI_P256, point)),
      publicKeyAlgorithm: -7,
    },
  };
}

/** The signature proving this vault holds the key. */
export function signWithPasskey(req: PasskeyRequest, key: StoredPasskey) {
  const o = req.options as PasskeyAskGet;
  const authData = concat(sha256(text(req.rpId)), Uint8Array.of(BASE_FLAGS), new Uint8Array(4));
  const data = clientData('webauthn.get', o.challenge, req.origin);
  const signed = concat(authData, sha256(data));
  const signature = p256.sign(signed, fromB64url(key.secret), { format: 'der', lowS: true });
  return {
    id: key.credentialId,
    clientDataJSON: toB64url(data),
    authenticatorData: toB64url(authData),
    signature: toB64url(signature),
    userHandle: key.userHandle,
  };
}
