// JSON with comments and trailing commas, as VS Code's own files are written.

export function stripJsonc(text: string): string {
  let out = '';
  let i = 0;
  while (i < text.length) {
    const c = text[i];
    if (c === '"') {
      const start = i++;
      while (i < text.length && text[i] !== '"') i += text[i] === '\\' ? 2 : 1;
      i++;
      out += text.slice(start, i);
    } else if (c === '/' && text[i + 1] === '/') {
      while (i < text.length && text[i] !== '\n') i++;
    } else if (c === '/' && text[i + 1] === '*') {
      const end = text.indexOf('*/', i + 2);
      i = end < 0 ? text.length : end + 2;
    } else if (c === ',') {
      let j = i + 1;
      // look past blanks and comments for a closing bracket
      for (;;) {
        while (j < text.length && /\s/.test(text[j])) j++;
        if (text[j] === '/' && text[j + 1] === '/') {
          while (j < text.length && text[j] !== '\n') j++;
        } else if (text[j] === '/' && text[j + 1] === '*') {
          const end = text.indexOf('*/', j + 2);
          j = end < 0 ? text.length : end + 2;
        } else {
          break;
        }
      }
      if (text[j] !== '}' && text[j] !== ']') out += c;
      i++;
    } else {
      out += c;
      i++;
    }
  }
  return out;
}

export function parseJsonc(text: string): unknown {
  return JSON.parse(stripJsonc(text.replace(/^﻿/, '')));
}
