// One change of a diff taken or undone on its own. Line numbers are Monaco's: from 1, and an end of
// 0 means no lines on that side, with the change right after the start line.

export interface LineChange {
  originalStartLineNumber: number;
  originalEndLineNumber: number;
  modifiedStartLineNumber: number;
  modifiedEndLineNumber: number;
}

function replaceLines(into: string, start: number, end: number, from: string, fromStart: number, fromEnd: number): string {
  const lines = into.split('\n');
  const incoming = fromEnd === 0 ? [] : from.split('\n').slice(fromStart - 1, fromEnd);
  if (end === 0) lines.splice(start, 0, ...incoming);
  else lines.splice(start - 1, end - start + 1, ...incoming);
  return lines.join('\n');
}

/** The old text with this change in it: what is left to review shrinks by this change. */
export const baseWithChange = (base: string, current: string, c: LineChange) =>
  replaceLines(base, c.originalStartLineNumber, c.originalEndLineNumber, current, c.modifiedStartLineNumber, c.modifiedEndLineNumber);

/** The new text without this change: the file with only this part undone. */
export const currentWithoutChange = (base: string, current: string, c: LineChange) =>
  replaceLines(current, c.modifiedStartLineNumber, c.modifiedEndLineNumber, base, c.originalStartLineNumber, c.originalEndLineNumber);
