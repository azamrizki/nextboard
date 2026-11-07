import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { pwnedCount } from './breach';

const sha1 = (s: string) => createHash('sha1').update(s).digest('hex').toUpperCase();

describe('pwnedCount', () => {
  it('sends only the first 5 characters of the hash, and compares the rest here', async () => {
    const h = sha1('password');
    const asked: { url: string; init: RequestInit }[] = [];
    const get = async (url: string, init: RequestInit) => {
      asked.push({ url, init });
      return new Response(`0000000000000000000000000000000000A:0\r\n${h.slice(5)}:9545824\r\nFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF:3\r\n`);
    };
    await expect(pwnedCount('password', get)).resolves.toBe(9545824);
    expect(asked).toHaveLength(1);
    expect(asked[0].url).toBe(`/api/vault/pwned/${h.slice(0, 5)}`);
    expect(asked[0].url).not.toContain(h.slice(5));
  });

  it('is 0 for a suffix that is missing or only padding', async () => {
    const h = sha1('a password nobody has');
    await expect(pwnedCount('a password nobody has', async () => new Response('0123456789ABCDEF0123456789ABCDEF012:4\n'))).resolves.toBe(0);
    await expect(pwnedCount('a password nobody has', async () => new Response(`${h.slice(5)}:0\n`))).resolves.toBe(0);
  });

  it('asks once per prefix with a cache, and says when the service fails', async () => {
    let calls = 0;
    const cache = new Map<string, string>();
    const get = async () => {
      calls++;
      return new Response('');
    };
    await pwnedCount('same', get, cache);
    await pwnedCount('same', get, cache);
    expect(calls).toBe(1);
    await expect(pwnedCount('x', async () => new Response('', { status: 503 }))).rejects.toThrow('503');
    await expect(pwnedCount('x', async () => new Response('{"error":"Have I Been Pwned is out of reach from the hub"}', { status: 502 }))).rejects.toThrow('out of reach');
  });
});
