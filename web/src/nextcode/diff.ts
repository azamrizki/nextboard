// The change an Edit, Write or MultiEdit makes, from the tool's own input: a line diff of the old
// and new text (no file is read). Small and exact for what agents usually change; a very large
// change is shown as all of the old lines out and all of the new ones in.

export interface DiffLine {
  op: ' ' | '+' | '-';
  text: string;
}

export interface Diff {
  lines: DiffLine[];
  added: number;
  removed: number;
}

/** Above this many lines on one side, no line matching is tried. */
const MAX_LINES = 600;

const split = (s: string) => (s === '' ? [] : s.replace(/\r\n/g, '\n').split('\n'));

/** A line diff of two texts (longest common subsequence). */
export function diffLines(before: string, after: string): DiffLine[] {
  const a = split(before);
  const b = split(after);
  // the same start and end need no matching
  let head = 0;
  while (head < a.length && head < b.length && a[head] === b[head]) head++;
  let tail = 0;
  while (tail < a.length - head && tail < b.length - head && a[a.length - 1 - tail] === b[b.length - 1 - tail]) tail++;
  const out: DiffLine[] = a.slice(0, head).map((text) => ({ op: ' ', text }));
  const x = a.slice(head, a.length - tail);
  const y = b.slice(head, b.length - tail);
  if (x.length > MAX_LINES || y.length > MAX_LINES || x.length * y.length > 250_000) {
    for (const text of x) out.push({ op: '-', text });
    for (const text of y) out.push({ op: '+', text });
  } else {
    // lcs[i][j]: the longest common run of x[i:] and y[j:]
    const n = x.length;
    const m = y.length;
    const lcs: Uint16Array[] = Array.from({ length: n + 1 }, () => new Uint16Array(m + 1));
    for (let i = n - 1; i >= 0; i--) {
      for (let j = m - 1; j >= 0; j--) lcs[i][j] = x[i] === y[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
    let i = 0;
    let j = 0;
    while (i < n || j < m) {
      if (i < n && j < m && x[i] === y[j]) {
        out.push({ op: ' ', text: x[i] });
        i++;
        j++;
      } else if (j < m && (i >= n || lcs[i][j + 1] >= lcs[i + 1][j])) {
        out.push({ op: '+', text: y[j] });
        j++;
      } else {
        out.push({ op: '-', text: x[i] });
        i++;
      }
    }
    // a run of changes reads better with what went out before what came in
    reorderRuns(out, head);
  }
  for (const text of a.slice(a.length - tail)) out.push({ op: ' ', text });
  return out;
}

/** Within each run of changed lines, the removed lines first. */
function reorderRuns(lines: DiffLine[], from: number) {
  let i = from;
  while (i < lines.length) {
    if (lines[i].op === ' ') {
      i++;
      continue;
    }
    let j = i;
    while (j < lines.length && lines[j].op !== ' ') j++;
    const run = lines.slice(i, j);
    const sorted = [...run.filter((l) => l.op === '-'), ...run.filter((l) => l.op === '+')];
    lines.splice(i, run.length, ...sorted);
    i = j;
  }
}

/** Only the changed lines and a few around them; `null` marks a gap. */
export function hunks(lines: DiffLine[], context = 2): (DiffLine | null)[] {
  const keep = new Uint8Array(lines.length);
  lines.forEach((l, i) => {
    if (l.op === ' ') return;
    for (let k = Math.max(0, i - context); k <= Math.min(lines.length - 1, i + context); k++) keep[k] = 1;
  });
  const out: (DiffLine | null)[] = [];
  let gap = false;
  lines.forEach((l, i) => {
    if (keep[i]) {
      if (gap && out.length) out.push(null);
      gap = false;
      out.push(l);
    } else gap = true;
  });
  return out;
}

const str = (v: unknown) => (typeof v === 'string' ? v : '');

function count(lines: DiffLine[]): Diff {
  let added = 0;
  let removed = 0;
  for (const l of lines) {
    if (l.op === '+') added++;
    else if (l.op === '-') removed++;
  }
  return { lines, added, removed };
}

/**
 * The diff of a file tool's input: Edit (old_string → new_string), MultiEdit (each of its edits,
 * one after the other), Write (all of content, as new lines). null for any other tool.
 */
export function toolDiff(name: string, input: unknown): Diff | null {
  const o = (input && typeof input === 'object' ? input : {}) as Record<string, unknown>;
  switch (name) {
    case 'Edit':
      return count(diffLines(str(o.old_string), str(o.new_string)));
    case 'MultiEdit': {
      const edits = Array.isArray(o.edits) ? (o.edits as Record<string, unknown>[]) : [];
      const lines: DiffLine[] = [];
      edits.forEach((e, i) => {
        if (i > 0) lines.push({ op: ' ', text: '⋯' });
        lines.push(...diffLines(str(e?.old_string), str(e?.new_string)));
      });
      return count(lines);
    }
    case 'Write':
      return count(split(str(o.content)).map((text) => ({ op: '+', text })));
    default:
      return null;
  }
}
