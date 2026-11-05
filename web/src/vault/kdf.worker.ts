// Argon2id off the main thread. It lives only for one derivation (crypto.ts terminates it).

import { argon2id } from 'hash-wasm';

interface Request {
  password: Uint8Array;
  salt: Uint8Array;
  m: number;
  t: number;
  p: number;
}

self.onmessage = async (e: MessageEvent<Request>) => {
  const { password, salt, m, t, p } = e.data;
  try {
    const hash = await argon2id({ password, salt, memorySize: m, iterations: t, parallelism: p, hashLength: 32, outputType: 'binary' });
    self.postMessage({ hash }, { transfer: [hash.buffer] });
  } catch (err) {
    self.postMessage({ error: err instanceof Error ? err.message : String(err) });
  } finally {
    password.fill(0);
  }
};
