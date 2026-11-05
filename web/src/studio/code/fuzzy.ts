// Non-contiguous matching for "Go to file": "uc" finds UserController.php. Letters of the
// file name count for more than letters of its folders; word starts and runs score extra.

export interface FuzzyHit {
  score: number;
  /** Matched character positions in the text. */
  positions: number[];
}

const isUpper = (c: string) => c >= 'A' && c <= 'Z';
const isSep = (c: string) => c === '/' || c === '_' || c === '-' || c === '.' || c === ' ';

/** Greedy subsequence match of q (lower case) in text; null when it does not fit. */
function matchIn(q: string, text: string, from: number): FuzzyHit | null {
  const lower = text.toLowerCase();
  // the last position each query letter may take: lets a later word start win
  let score = 0;
  let last = -2;
  let at = from;
  const positions: number[] = [];
  for (let i = 0; i < q.length; i++) {
    const c = q[i];
    let pick = lower.indexOf(c, at);
    if (pick < 0) return null;
    // prefer a word start within reach over an arbitrary letter, unless it extends a run
    if (pick !== last + 1) {
      for (let j = pick; j < lower.length && j < pick + 32; j++) {
        if (lower[j] !== c) continue;
        if (j === 0 || isSep(text[j - 1]) || (isUpper(text[j]) && !isUpper(text[j - 1]))) {
          pick = j;
          break;
        }
      }
    }
    if (pick === last + 1) score += 6;
    if (pick === 0 || isSep(text[pick - 1])) score += 8;
    else if (isUpper(text[pick]) && !isUpper(text[pick - 1])) score += 7;
    score -= Math.min(pick - at, 6) * 0.2;
    positions.push(pick);
    last = pick;
    at = pick + 1;
  }
  return { score, positions };
}

/** Score a path: a match inside the file name beats one spread over folders. */
export function fuzzyMatch(query: string, path: string): FuzzyHit | null {
  const q = query.replace(/\s+/g, '').toLowerCase();
  if (!q) return { score: 0, positions: [] };
  const base = path.lastIndexOf('/') + 1;
  const inName = matchIn(q, path, base);
  if (inName) {
    const exact = path.length - base === q.length ? 40 : 0;
    return { score: inName.score + 30 + exact - (path.length - base) * 0.05 - path.length * 0.01, positions: inName.positions };
  }
  const inPath = matchIn(q, path, 0);
  if (!inPath) return null;
  return { score: inPath.score - path.length * 0.05, positions: inPath.positions };
}

/** The best matches of many paths, best first. */
export function fuzzyTop(query: string, paths: readonly string[], limit: number): { path: string; hit: FuzzyHit }[] {
  const out: { path: string; hit: FuzzyHit }[] = [];
  for (const path of paths) {
    const hit = fuzzyMatch(query, path);
    if (hit) out.push({ path, hit });
  }
  out.sort((a, b) => b.hit.score - a.hit.score || a.path.length - b.path.length);
  return out.slice(0, limit);
}
