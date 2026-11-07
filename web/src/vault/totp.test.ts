import { describe, expect, it } from 'vitest';
import { formatCode, parseTotp, totpCode } from './totp';

const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
function base32(text: string) {
  let bits = '';
  for (const c of new TextEncoder().encode(text)) bits += c.toString(2).padStart(8, '0');
  while (bits.length % 5) bits += '0';
  return bits.replace(/.{5}/g, (b) => ALPHABET[parseInt(b, 2)]);
}

// RFC 6238, appendix B.
const SEEDS = { SHA1: '12345678901234567890', SHA256: '12345678901234567890123456789012', SHA512: '1234567890123456789012345678901234567890123456789012345678901234' };
const VECTORS: [number, string, string, string][] = [
  [59, '94287082', '46119246', '90693936'],
  [1111111109, '07081804', '68084774', '25091201'],
  [1111111111, '14050471', '67062674', '99943326'],
  [1234567890, '89005924', '91819424', '93441116'],
  [2000000000, '69279037', '90698825', '38618901'],
  [20000000000, '65353130', '77737706', '47863826'],
];

describe('Steam Guard', () => {
  // worked out apart, with Python's hmac
  it.each([
    [59, '2YXGV'],
    [1111111109, 'CWDGV'],
    [2000000000, 'HNCVQ'],
  ])('at %i', async (time, want) => {
    for (const key of ['steam://JBSWY3DPEHPK3PXP', 'otpauth://totp/Steam:me?secret=JBSWY3DPEHPK3PXP&issuer=Steam&encoder=steam']) {
      const spec = parseTotp(key);
      expect(spec?.steam).toBe(true);
      await expect(totpCode(spec!, time * 1000)).resolves.toBe(want);
    }
  });
  it('is shown whole', () => {
    expect(formatCode('2YXGV')).toBe('2YXGV');
    expect(formatCode('123456')).toBe('123 456');
  });
  it('refuses a key that is no Base32', () => {
    expect(parseTotp('steam://not-base32!')).toBeNull();
  });
});

describe('totpCode', () => {
  it.each(VECTORS)('matches RFC 6238 at %i', async (time, sha1, sha256, sha512) => {
    for (const [algo, want] of [['SHA1', sha1], ['SHA256', sha256], ['SHA512', sha512]] as const) {
      const spec = parseTotp(`otpauth://totp/x?secret=${base32(SEEDS[algo])}&digits=8&algorithm=${algo}`);
      expect(spec).not.toBeNull();
      await expect(totpCode(spec!, time * 1000)).resolves.toBe(want);
    }
  });

  it('keeps leading zeros and follows the period', async () => {
    const spec = parseTotp(`otpauth://totp/x?secret=${base32(SEEDS.SHA1)}&digits=8`)!;
    await expect(totpCode(spec, 1111111109_000)).resolves.toBe('07081804');
    const slow = parseTotp(`otpauth://totp/x?secret=${base32(SEEDS.SHA1)}&period=60`)!;
    await expect(totpCode(slow, 60_000)).resolves.toBe(await totpCode(slow, 119_000));
    await expect(totpCode(slow, 60_000)).resolves.not.toBe(await totpCode(slow, 120_000));
  });
});

describe('parseTotp', () => {
  it('takes a bare key the way people paste it', () => {
    const want = parseTotp('GEZDGNBVGY3TQOJQ')!.secret;
    for (const v of ['gezd gnbv gy3t qojq', 'GEZD-GNBV-GY3T-QOJQ', ' GEZDGNBVGY3TQOJQ ', 'GEZDGNBVGY3TQOJQ====']) expect(parseTotp(v)?.secret).toEqual(want);
    expect(parseTotp('GEZDGNBVGY3TQOJQ')).toMatchObject({ digits: 6, period: 30, algorithm: 'SHA-1' });
  });

  it.each([
    ['nothing', ''],
    ['not base32', 'not base32 at all!'],
    ['digits outside base32', '1890'],
    ['too short for one byte', 'A'],
    ['another scheme', 'https://example.com/?secret=GEZDGNBV'],
    ['hotp', 'otpauth://hotp/x?secret=GEZDGNBV&counter=1'],
    ['no secret', 'otpauth://totp/x'],
    ['too few digits', 'otpauth://totp/x?secret=GEZDGNBV&digits=4'],
    ['too many digits', 'otpauth://totp/x?secret=GEZDGNBV&digits=11'],
    ['no period', 'otpauth://totp/x?secret=GEZDGNBV&period=0'],
    ['a period that is no number', 'otpauth://totp/x?secret=GEZDGNBV&period=abc'],
    ['an unknown algorithm', 'otpauth://totp/x?secret=GEZDGNBV&algorithm=MD5'],
    ['a broken uri', 'otpauth://[bad'],
  ])('refuses %s', (_name, raw) => {
    expect(parseTotp(raw)).toBeNull();
  });
});

it('formatCode splits a code in the middle', () => {
  expect(formatCode('123456')).toBe('123 456');
  expect(formatCode('12345678')).toBe('1234 5678');
});
