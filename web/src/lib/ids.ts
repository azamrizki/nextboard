// Random ids for board nodes and their inner items.
// Board ids must match ^[a-z]_[a-z0-9]{6,24}$ (docs/API-board.md §2).

const ALPHABET = 'abcdefghijklmnopqrstuvwxyz0123456789';

function randomChars(len: number): string {
  const bytes = new Uint8Array(len);
  crypto.getRandomValues(bytes);
  let out = '';
  for (const b of bytes) out += ALPHABET[b % ALPHABET.length];
  return out;
}

/** "n_k3j2h1q9z8" style id: one-letter prefix + 10 random chars. */
export function newId(prefix: 'n' | 'e' | 'i' = 'n', len = 10): string {
  return `${prefix}_${randomChars(len)}`;
}

/** Short id for items inside a node's data (checklist items, kanban cards, events). */
export function itemId(): string {
  return randomChars(8);
}

/**
 * Identifies this tab when posting board ops, so the hub's echo of our own ops can be
 * recognised (and not applied twice). New per page load.
 */
export const CLIENT_ID = `c_${randomChars(12)}`;
