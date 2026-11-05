// Password and passphrase generator. Every pick comes from crypto.getRandomValues with rejection
// sampling: a plain modulo would make some characters likelier than others.

export interface PasswordOptions {
  length: number;
  upper: boolean;
  lower: boolean;
  digits: boolean;
  symbols: boolean;
  avoidAmbiguous: boolean;
}

export interface PassphraseOptions {
  words: number;
  separator: string;
  capitalize: boolean;
  number: boolean;
}

export const DEFAULT_PASSWORD: PasswordOptions = { length: 20, upper: true, lower: true, digits: true, symbols: true, avoidAmbiguous: false };
export const DEFAULT_PASSPHRASE: PassphraseOptions = { words: 6, separator: '-', capitalize: false, number: false };

const SETS = {
  upper: 'ABCDEFGHIJKLMNOPQRSTUVWXYZ',
  lower: 'abcdefghijklmnopqrstuvwxyz',
  digits: '0123456789',
  symbols: '!@#$%^&*-_=+?',
};
const AMBIGUOUS = /[Il1O0]/g;

/** An unbiased integer in [0, n). */
export function randomBelow(n: number): number {
  const limit = Math.floor(0x100000000 / n) * n;
  const box = new Uint32Array(1);
  do {
    crypto.getRandomValues(box);
  } while (box[0] >= limit);
  return box[0] % n;
}

function shuffle<T>(list: T[]): T[] {
  for (let i = list.length - 1; i > 0; i--) {
    const j = randomBelow(i + 1);
    [list[i], list[j]] = [list[j], list[i]];
  }
  return list;
}

function chosenSets(o: PasswordOptions): string[] {
  const sets = [o.upper && SETS.upper, o.lower && SETS.lower, o.digits && SETS.digits, o.symbols && SETS.symbols].filter((s): s is string => !!s);
  return o.avoidAmbiguous ? sets.map((s) => s.replace(AMBIGUOUS, '')) : sets;
}

/** One from every chosen set, the rest from all of them, in random order. */
export function generatePassword(o: PasswordOptions): string {
  const sets = chosenSets(o);
  if (!sets.length) return '';
  const all = sets.join('');
  const out = sets.map((s) => s[randomBelow(s.length)]);
  while (out.length < o.length) out.push(all[randomBelow(all.length)]);
  return shuffle(out).join('');
}

export function passwordBits(o: PasswordOptions): number {
  const pool = chosenSets(o).join('').length;
  return pool ? Math.floor(o.length * Math.log2(pool)) : 0;
}

/** The BIP-39 list: 2048 audited words, with no two alike in their first four letters. */
async function wordlist(): Promise<string[]> {
  const m = await import('@scure/bip39/wordlists/english.js');
  return m.wordlist;
}

export async function generatePassphrase(o: PassphraseOptions): Promise<string> {
  const list = await wordlist();
  const words = Array.from({ length: o.words }, () => {
    const w = list[randomBelow(list.length)];
    return o.capitalize ? w[0].toUpperCase() + w.slice(1) : w;
  });
  if (o.number) {
    const i = randomBelow(words.length);
    words[i] += String(randomBelow(10));
  }
  return words.join(o.separator);
}

export const passphraseBits = (o: PassphraseOptions) => o.words * 11;

// ---- how strong a master password is ----

export interface Strength {
  /** 0 to 4. */
  score: number;
  label: string;
  /** What to do about it, when there is something to do. */
  advice: string;
}

/** A master password must be at least this long. How strong it is, the meter says and the owner decides. */
export const MASTER_MIN_LENGTH = 8;
/** Below this the meter says what to do about it. */
export const WEAK_BELOW_SCORE = 3;

const LABELS = ['Very weak', 'Weak', 'Fair', 'Strong', 'Very strong'];

/**
 * zxcvbn knows the common passwords, names and keyboard walks a length rule would let through.
 * It only loads where a master password is being chosen.
 */
export async function passwordStrength(pw: string): Promise<Strength> {
  const { default: zxcvbn } = await import('zxcvbn');
  // It gets slow on long input, and that long is strong anyway.
  const r = zxcvbn(pw.slice(0, 100), ['nextboard', 'vault', 'credentials', 'password']);
  const score = r.score as number;
  return { score, label: LABELS[score], advice: r.feedback.warning || r.feedback.suggestions[0] || '' };
}
