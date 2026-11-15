// What a task's output says: where it failed and which tests did. Compilers and test runners
// write much the same shapes, so these few cover most of them.

export interface TaskProblem {
  file: string;
  line: number;
  col: number;
  message: string;
}

const ANSI = /\u001b\[[0-9;?]*[ -/]*[@-~]|\u001b\][^\u0007\u001b]*(?:\u0007|\u001b\\)/g;

export const stripAnsi = (s: string) => s.replace(ANSI, '');

/** A sequence still open at the end of what has come, which the next chunk may finish. */
const OPEN = /\u001b(?:\[[0-9;?]*[ -/]*|\][^\u0007\u001b]{0,4096}\u001b?)?$/;
/** Shortest run of text trusted to find where a stream goes on after its start was dropped. */
const OVERLAP = 64;

/** One stream's text without escape sequences, kept between calls of stripAnsiTail. */
export interface AnsiTail {
  id: string;
  /** The raw text last seen. */
  raw: string;
  /** raw up to here is in clean; after it a sequence is still open. */
  done: number;
  /** The text without escape sequences, its last `keep` chars. */
  clean: string;
}

/**
 * stripAnsi(raw), its last `keep` chars, stripping only what came since the last call: the
 * Tasks tab got a chunk every few ms and stripped all 512 KB each time. raw is a stream's text
 * so far; the store may have dropped its start, so it can begin later than the last raw did.
 */
export function stripAnsiTail(prev: AnsiTail | null, id: string, raw: string, keep: number): AnsiTail {
  if (prev && prev.id === id && prev.raw === raw) return prev;
  let clean = '';
  let from = 0;
  if (prev && prev.id === id) {
    const dropped = raw.startsWith(prev.raw) ? 0 : droppedFrom(prev.raw, raw);
    if (dropped >= 0) {
      clean = prev.clean;
      from = Math.max(0, prev.done - dropped);
    }
  }
  const open = OPEN.exec(raw.slice(from));
  const done = open ? from + open.index : raw.length;
  clean += stripAnsi(raw.slice(from, done));
  if (clean.length > keep) clean = clean.slice(clean.length - keep);
  return { id, raw, done, clean };
}

/** How many chars of prev's start are gone when raw goes on from prev's end; -1 when it does not. */
function droppedFrom(prev: string, raw: string): number {
  const head = raw.slice(0, OVERLAP);
  if (head.length < OVERLAP) return -1;
  // a repeating output can match early; each try is checked, and a few are enough
  for (let k = prev.indexOf(head), tries = 0; k >= 0 && tries < 8; k = prev.indexOf(head, k + 1), tries++) {
    if (raw.startsWith(prev.slice(k))) return k;
  }
  return -1;
}

const LOCATION = /^\s*(?:-->\s*|at\s+(?:.*\()?)?((?:[A-Za-z]:)?[\w@./\\ -]*\.[A-Za-z0-9]+):(\d+)(?::(\d+))?\)?(?::?\s+(.*))?$/;
const TSC = /^\s*(.+\.[A-Za-z0-9]+)\((\d+),(\d+)\):\s*(.*)$/;
const PYTHON = /^\s*File "(.+)", line (\d+)/;

const FAILED_TEST = [/^\s*--- FAIL: (\S+)/, /^\s*(?:✕|×|✗)\s+(.+?)(?:\s+\(\d+\s*m?s\))?$/, /^\s*FAILED\s+(\S+)/, /^\s*FAIL\s+(\S+\.\S+)/, /^\s*test (\S+) \.\.\. FAILED$/];

const MAX = 200;

export function parseTaskOutput(raw: string): { problems: TaskProblem[]; failed: string[] } {
  const problems: TaskProblem[] = [];
  const failed: string[] = [];
  const seen = new Set<string>();
  for (const line of stripAnsi(raw).split('\n')) {
    if (problems.length >= MAX) break;
    for (const re of FAILED_TEST) {
      const m = re.exec(line);
      if (m && !failed.includes(m[1])) failed.push(m[1]);
    }
    const t = TSC.exec(line);
    const p = PYTHON.exec(line);
    const l = t || p ? null : LOCATION.exec(line);
    const hit = t ? { file: t[1], line: t[2], col: t[3], message: t[4] } : p ? { file: p[1], line: p[2], col: '1', message: '' } : l ? { file: l[1], line: l[2], col: l[3] ?? '1', message: l[4] ?? '' } : null;
    if (!hit || /node_modules|^node:|^internal\//.test(hit.file)) continue;
    const key = `${hit.file}:${hit.line}:${hit.col}`;
    if (seen.has(key)) continue;
    seen.add(key);
    problems.push({ file: hit.file, line: Number(hit.line), col: Number(hit.col), message: hit.message });
  }
  return { problems, failed };
}
