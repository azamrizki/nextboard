import { describe, expect, it } from 'vitest';
import { advance, cutAt, LAG_LIVE_MS, MIN_RATE, splitBlocks } from './smooth';

describe('advance', () => {
  it('spreads a burst over the frames after it, and is done within about the lag', () => {
    let shown = 0;
    let frames = 0;
    while (shown < 400 && frames < 1000) {
      shown = advance(shown, 400, 16, false);
      frames++;
    }
    expect(shown).toBe(400);
    expect(frames).toBeGreaterThan(5); // not all at once
    expect(frames * 16).toBeLessThan(LAG_LIVE_MS * 6); // and not slowly
  });
  it('goes on at the least rate at the end of a burst, and never past the text', () => {
    expect(advance(98, 100, 16, false)).toBeCloseTo(98 + (MIN_RATE * 16) / 1000, 5);
    expect(advance(100, 100, 16, false)).toBe(100);
    expect(advance(0, 3, 1000, false)).toBeLessThanOrEqual(3);
  });
  it('drains faster once the reply is complete', () => {
    expect(advance(0, 500, 16, true)).toBeGreaterThan(advance(0, 500, 16, false));
  });
  it('does not show a whole burst after a long pause', () => {
    expect(advance(0, 2000, 5000, false)).toBeLessThan(2000);
  });
});

describe('cutAt', () => {
  it('never cuts a character outside the BMP in half', () => {
    const s = 'ok 😀 done';
    expect(cutAt(s, 4)).toBe(5);
    expect(cutAt(s, 5)).toBe(5);
    expect(cutAt(s, 0)).toBe(0);
    expect(cutAt(s, 99)).toBe(s.length);
  });
});

describe('splitBlocks', () => {
  it('cuts at blank lines between blocks that start at the margin', () => {
    expect(splitBlocks('# Title\n\nFirst para.\n\nSecond\nline')).toEqual({ done: ['# Title\n', 'First para.\n'], tail: 'Second\nline' });
  });
  it('keeps a code fence whole, blank lines and all', () => {
    const t = 'Look:\n\n```go\nfunc a() {}\n\nfunc b() {}\n```\n\nAfter\n';
    expect(splitBlocks(t)).toEqual({ done: ['Look:\n', '```go\nfunc a() {}\n\nfunc b() {}\n```\n'], tail: 'After\n' });
    expect(splitBlocks('```\nopen\n\nstill open').done).toEqual([]);
  });
  it('keeps a loose list and indented continuations together', () => {
    expect(splitBlocks('- a\n\n- b\n\n  more of b\n').done).toEqual([]);
    expect(splitBlocks('1. a\n\n2. b\n\nNext\nline').done).toEqual(['1. a\n\n2. b\n']);
  });
  it('does not cut on a line still being written', () => {
    expect(splitBlocks('Para.\n\n1')).toEqual({ done: [], tail: 'Para.\n\n1' });
  });
  it('gives the text back whole when joined', () => {
    const t = 'A\n\nB\n\n```\nc\n\nd\n```\n\n- e\n\n- f\n\nG\nH';
    const { done, tail } = splitBlocks(t);
    expect([...done, tail].join('\n')).toBe(t);
  });
});
