import { describe, expect, it } from 'vitest';
import { DEFAULT_PASSPHRASE, DEFAULT_PASSWORD, generatePassphrase, generatePassword, passphraseBits, passwordBits, randomBelow } from './generator';

describe('generatePassword', () => {
  it('has the length asked for and one of every chosen kind', () => {
    for (let i = 0; i < 200; i++) {
      const p = generatePassword(DEFAULT_PASSWORD);
      expect(p).toHaveLength(20);
      expect(p).toMatch(/[A-Z]/);
      expect(p).toMatch(/[a-z]/);
      expect(p).toMatch(/[0-9]/);
      expect(p).toMatch(/[!@#$%^&*\-_=+?]/);
    }
  });

  it('leaves out what is not chosen', () => {
    expect(generatePassword({ ...DEFAULT_PASSWORD, upper: false, symbols: false })).toMatch(/^[a-z0-9]{20}$/);
    expect(generatePassword({ ...DEFAULT_PASSWORD, upper: false, lower: false, digits: false, symbols: false })).toBe('');
  });

  it('can avoid characters people mix up', () => {
    for (let i = 0; i < 200; i++) expect(generatePassword({ ...DEFAULT_PASSWORD, avoidAmbiguous: true, length: 64 })).not.toMatch(/[Il1O0]/);
  });

  it('does not repeat itself', () => {
    expect(new Set(Array.from({ length: 500 }, () => generatePassword(DEFAULT_PASSWORD))).size).toBe(500);
  });

  it('counts its strength from the characters in play', () => {
    expect(passwordBits({ ...DEFAULT_PASSWORD, upper: false, symbols: false, length: 10 })).toBe(Math.floor(10 * Math.log2(36)));
    expect(passwordBits({ ...DEFAULT_PASSWORD, upper: false, lower: false, digits: false, symbols: false })).toBe(0);
  });
});

describe('randomBelow', () => {
  it('stays in range and reaches all of it', () => {
    const seen = new Set<number>();
    for (let i = 0; i < 2000; i++) {
      const n = randomBelow(7);
      expect(n).toBeGreaterThanOrEqual(0);
      expect(n).toBeLessThan(7);
      seen.add(n);
    }
    expect(seen.size).toBe(7);
    expect(randomBelow(1)).toBe(0);
  });

  it('shows no bias a modulo would', () => {
    // 3 does not divide 2^32 evenly; 30 000 draws land near 10 000 each.
    const counts = [0, 0, 0];
    for (let i = 0; i < 30000; i++) counts[randomBelow(3)]++;
    for (const c of counts) expect(Math.abs(c - 10000)).toBeLessThan(500);
  });
});

describe('generatePassphrase', () => {
  it('has the words asked for, from the list', async () => {
    const p = await generatePassphrase(DEFAULT_PASSPHRASE);
    expect(p.split('-')).toHaveLength(6);
    expect(p).toMatch(/^[a-z-]+$/);
    expect(passphraseBits(DEFAULT_PASSPHRASE)).toBe(66);
  });

  it('follows its options', async () => {
    const p = await generatePassphrase({ words: 4, separator: ' ', capitalize: true, number: true });
    const words = p.split(' ');
    expect(words).toHaveLength(4);
    for (const w of words) expect(w).toMatch(/^[A-Z][a-z]+\d?$/);
    expect(p).toMatch(/\d/);
  });
});
