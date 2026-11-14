import { describe, expect, it } from 'vitest';
import { OUTPUT_JOBS, OUTPUT_KEEP, withOutput } from './store';

describe('job output kept in a tab', () => {
  it('keeps the end of a long output', () => {
    let out: Record<string, string> = {};
    out = withOutput(out, 'j', 'a'.repeat(OUTPUT_KEEP));
    out = withOutput(out, 'j', 'the end');
    expect(out.j).toHaveLength(OUTPUT_KEEP);
    expect(out.j.endsWith('the end')).toBe(true);
  });

  it('keeps the newest jobs, the one written to among them', () => {
    let out: Record<string, string> = {};
    for (let i = 0; i < OUTPUT_JOBS + 20; i++) out = withOutput(out, `j${i}`, `out ${i}`);
    expect(Object.keys(out)).toHaveLength(OUTPUT_JOBS);
    expect(out.j0).toBeUndefined();
    expect(out[`j${OUTPUT_JOBS + 19}`]).toBe(`out ${OUTPUT_JOBS + 19}`);
    // an old job that writes again stays, and goes to the end of the line
    const oldest = Object.keys(out)[0];
    out = withOutput(out, oldest, ' more');
    expect(out[oldest].endsWith(' more')).toBe(true);
    out = withOutput(out, 'new', 'x');
    expect(out[oldest]).toBeDefined();
  });
});
