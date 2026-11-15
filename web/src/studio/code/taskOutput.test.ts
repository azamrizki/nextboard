import { describe, expect, it } from 'vitest';
import { stripAnsi, stripAnsiTail, type AnsiTail } from './taskOutput';

/** Feed chunks the way the store grows a job's output, the start dropped past `cap`. */
function feed(chunks: string[], keep: number, cap = Infinity) {
  let raw = '';
  let tail: AnsiTail | null = null;
  for (const c of chunks) {
    raw += c;
    if (raw.length > cap) raw = raw.slice(raw.length - cap);
    tail = stripAnsiTail(tail, 'j', raw, keep);
  }
  return { raw, clean: tail?.clean ?? '' };
}

describe('stripAnsiTail', () => {
  it('waits for a sequence cut between chunks', () => {
    expect(feed(['abc\u001b[3', '1mdef\u001b', '[0mghi'], 1000).clean).toBe('abcdefghi');
    expect(feed(['x\u001b]0;ti', 'tle\u001b', '\\y'], 1000).clean).toBe('xy');
    expect(feed(['a\u001b]8;;http://x', '\u0007link\u001b]8;;\u0007b'], 1000).clean).toBe('alinkb');
  });

  it('gives what stripping the whole text gives, however it is cut', () => {
    const line = (i: number) => `\u001b[1;3${i % 8}m${i}\u001b[0m ok \u001b]0;t${i}\u0007src/a.ts:${i}:1\n`;
    const full = Array.from({ length: 400 }, (_, i) => line(i)).join('');
    for (const size of [1, 3, 7, 50, 333]) {
      const chunks: string[] = [];
      for (let i = 0; i < full.length; i += size) chunks.push(full.slice(i, i + size));
      expect(feed(chunks, 1 << 20).clean).toBe(stripAnsi(full));
      expect(feed(chunks, 500).clean).toBe(stripAnsi(full).slice(-500));
    }
  });

  it('goes on when the store drops the start of the output', () => {
    const chunks = Array.from({ length: 300 }, (_, i) => `\u001b[32mline ${i} of the build\u001b[0m\n`);
    const { raw, clean } = feed(chunks, 2000, 4096);
    expect(raw.length).toBe(4096);
    expect(clean).toBe(stripAnsi(raw).slice(-2000));
    // it went on from what it had, rather than stripping the kept 4 KB again
    const long = feed(chunks, 1 << 20, 4096).clean;
    expect(long.length).toBe(stripAnsi(chunks.join('')).length);
  });

  it('starts over for another job', () => {
    const a = stripAnsiTail(null, 'a', 'one\u001b[0m', 100);
    expect(stripAnsiTail(a, 'b', 'two', 100).clean).toBe('two');
    expect(stripAnsiTail(a, 'a', 'one\u001b[0m', 100)).toBe(a);
  });
});
