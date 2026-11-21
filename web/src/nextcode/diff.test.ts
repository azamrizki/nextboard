import { describe, expect, it } from 'vitest';
import { diffLines, hunks, toolDiff } from './diff';

const ops = (lines: { op: string; text: string }[]) => lines.map((l) => `${l.op}${l.text}`);

describe('diffLines', () => {
  it('marks what changed and keeps the rest', () => {
    expect(ops(diffLines('a\nb\nc', 'a\nB\nc'))).toEqual([' a', '-b', '+B', ' c']);
  });

  it('adds and removes whole lines', () => {
    expect(ops(diffLines('', 'x\ny'))).toEqual(['+x', '+y']);
    expect(ops(diffLines('x\ny', ''))).toEqual(['-x', '-y']);
    expect(ops(diffLines('a\nc', 'a\nb\nc'))).toEqual([' a', '+b', ' c']);
  });

  it('puts removed lines before added ones within a change', () => {
    expect(ops(diffLines('one\ntwo\nkeep', 'uno\ndos\nkeep'))).toEqual(['-one', '-two', '+uno', '+dos', ' keep']);
  });

  it('gives up matching on a very large change', () => {
    const big = Array.from({ length: 700 }, (_, i) => `l${i}`).join('\n');
    const out = diffLines(big, 'other');
    expect(out.filter((l) => l.op === '-')).toHaveLength(700);
    expect(out.filter((l) => l.op === '+')).toHaveLength(1);
  });
});

describe('hunks', () => {
  it('keeps the changes and a little around them, with gaps', () => {
    const lines = diffLines(['1', '2', '3', '4', '5', '6', '7', '8', '9'].join('\n'), ['1', '2', '3', '4', 'X', '6', '7', '8', '9'].join('\n'));
    const h = hunks(lines, 1);
    expect(h.map((l) => (l ? `${l.op}${l.text}` : '…'))).toEqual([' 4', '-5', '+X', ' 6']);
    const two = hunks(diffLines('a\nb\nc\nd\ne\nf\ng', 'A\nb\nc\nd\ne\nf\nG'), 1);
    expect(two.map((l) => (l ? `${l.op}${l.text}` : '…'))).toEqual(['-a', '+A', ' b', '…', ' f', '-g', '+G']);
  });
});

describe('toolDiff', () => {
  it('reads Edit, MultiEdit and Write inputs', () => {
    const edit = toolDiff('Edit', { file_path: '/x.ts', old_string: 'a\nb', new_string: 'a\nc\nd' });
    expect(edit).toMatchObject({ added: 2, removed: 1 });
    const multi = toolDiff('MultiEdit', { file_path: '/x.ts', edits: [{ old_string: 'a', new_string: 'b' }, { old_string: 'c', new_string: '' }] });
    expect(multi).toMatchObject({ added: 1, removed: 2 });
    expect(ops(multi!.lines)).toEqual(['-a', '+b', ' ⋯', '-c']);
    expect(toolDiff('Write', { file_path: '/x.ts', content: 'one\ntwo\n' })).toMatchObject({ added: 3, removed: 0 });
  });

  it('is null for other tools and survives bad input', () => {
    expect(toolDiff('Bash', { command: 'ls' })).toBeNull();
    expect(toolDiff('Edit', null)).toMatchObject({ added: 0, removed: 0 });
    expect(toolDiff('MultiEdit', { edits: 'nope' })).toMatchObject({ lines: [] });
  });
});
