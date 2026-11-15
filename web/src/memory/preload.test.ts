import { describe, expect, it } from 'vitest';
import { freshOnce } from './preload';

describe('freshOnce', () => {
  const setup = () => {
    let calls = 0;
    let t = 0;
    const once = freshOnce(
      () => Promise.resolve(++calls),
      5_000,
      () => t,
    );
    return { once, calls: () => calls, at: (ms: number) => (t = ms) };
  };

  it('hands the page what was fetched early, once', async () => {
    const s = setup();
    s.once.start();
    s.once.start(); // a press, then the route: one request
    expect(s.calls()).toBe(1);
    s.at(1_000);
    await expect(s.once.take()).resolves.toBe(1);
    // the next look asks again
    await expect(s.once.take()).resolves.toBe(2);
  });

  it('asks again when the early answer is old', async () => {
    const s = setup();
    s.once.start();
    s.at(6_000);
    await expect(s.once.take()).resolves.toBe(2);
  });

  it('asks when nothing was fetched early', async () => {
    const s = setup();
    await expect(s.once.take()).resolves.toBe(1);
  });

  it('passes on a failed early answer to whoever takes it', async () => {
    const once = freshOnce(() => Promise.reject(new Error('hub away')), 5_000);
    once.start();
    await expect(once.take()).rejects.toThrow('hub away');
  });
});
