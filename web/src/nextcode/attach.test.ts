import { describe, expect, it } from 'vitest';
import { attachedMessage, splitAttachedLine } from './attach';

const A = '/home/me/.nextboard/attachments/s_abc/screen.png';
const B = '/home/me/.nextboard/attachments/s_abc/notes.txt';

describe('attachedMessage', () => {
  it('puts the files on a final line after the text', () => {
    expect(attachedMessage('Why does this fail?', [A, B])).toBe(`Why does this fail?\n\nAttached: ${A}, ${B}`);
  });

  it('may be only files', () => {
    expect(attachedMessage('  ', [A])).toBe(`Attached: ${A}`);
  });

  it('is the text alone, trimmed, without files', () => {
    expect(attachedMessage('  fix the test \n', [])).toBe('fix the test');
  });
});

describe('splitAttachedLine', () => {
  it('takes the files back out of a message', () => {
    expect(splitAttachedLine(attachedMessage('Look at these', [A, B]))).toEqual({
      text: 'Look at these',
      files: [
        { path: A, name: 'screen.png' },
        { path: B, name: 'notes.txt' },
      ],
    });
  });

  it('reads a message of files only', () => {
    expect(splitAttachedLine(`Attached: ${A}`)).toEqual({ text: '', files: [{ path: A, name: 'screen.png' }] });
  });

  it('reads the paste folder of a console on a server too', () => {
    const P = '/tmp/nb-paste-1000-h1-s_abc/log.txt';
    expect(splitAttachedLine(`Here\n\nAttached: ${P}`)).toEqual({ text: 'Here', files: [{ path: P, name: 'log.txt' }] });
  });

  it('leaves text that only looks like it', () => {
    const own = 'Attached: the report you asked for';
    expect(splitAttachedLine(own)).toEqual({ text: own, files: [] });
    const outside = 'see\n\nAttached: /etc/hosts';
    expect(splitAttachedLine(outside)).toEqual({ text: outside, files: [] });
    const midLine = `I wrote Attached: ${A}`;
    expect(splitAttachedLine(midLine)).toEqual({ text: midLine, files: [] });
    expect(splitAttachedLine('plain')).toEqual({ text: 'plain', files: [] });
    const notLast = `Attached: ${A}\nand more`;
    expect(splitAttachedLine(notLast)).toEqual({ text: notLast, files: [] });
  });
});
