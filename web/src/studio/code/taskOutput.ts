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
