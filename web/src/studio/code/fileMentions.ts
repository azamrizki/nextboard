// `@path` in a question: finding the word being typed, and the files a finished question names.

/** The `@word` the caret is in, if any: `start` is the position of the `@`. */
export function mentionAt(text: string, caret: number): { start: number; query: string } | null {
  const before = text.slice(0, caret);
  const start = before.lastIndexOf('@');
  if (start < 0) return null;
  if (start > 0 && !/\s/.test(before[start - 1])) return null;
  const query = before.slice(start + 1);
  return /\s/.test(query) ? null : { start, query };
}

/** The word after the caret also goes when a suggestion replaces the one being typed. */
export function insertMention(text: string, caret: number, start: number, path: string): { text: string; caret: number } {
  let end = caret;
  while (end < text.length && !/\s/.test(text[end])) end++;
  const token = `@${path} `;
  return { text: text.slice(0, start) + token + text.slice(end).replace(/^\s/, ''), caret: start + token.length };
}

/** The project files a text names with `@`, in order, each once. */
export function mentionedFiles(text: string, known: ReadonlySet<string>): string[] {
  const out: string[] = [];
  for (const m of text.matchAll(/(?:^|\s)@(\S+)/g)) {
    const path = m[1].replace(/[.,;:!?)]+$/, '');
    if (known.has(path) && !out.includes(path)) out.push(path);
  }
  return out;
}
