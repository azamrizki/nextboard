// SQL in the editor's colours, for what shows SQL outside an editor: SmartDB's queries, the
// dialogs that ask before a statement runs.

import type { ReactNode } from 'react';

const KW = /^(select|from|where|and|or|not|group|by|order|limit|offset|join|left|right|inner|outer|on|as|in|is|null|count|sum|avg|min|max|distinct|having|union|all|case|when|then|else|end|insert|into|values|update|set|delete|create|table|alter|drop|with|desc|asc|like|between|exists|date|now|interval|cast|coalesce)$/i;

/** SQL in the editor's colours: keywords, strings, numbers. */
export function hlSQL(sql: string): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /('(?:[^']|'')*'|"[^"]*"|`[^`]*`|--[^\n]*|\b\d+(?:\.\d+)?\b|\b[A-Za-z_]+\b|\s+|.)/g;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(sql))) {
    const t = m[0];
    if (t.startsWith("'")) out.push(<span key={i++} className="hl-str">{t}</span>);
    else if (t.startsWith('--')) out.push(<span key={i++} className="hl-com">{t}</span>);
    else if (/^\d/.test(t)) out.push(<span key={i++} className="hl-num">{t}</span>);
    else if (KW.test(t)) out.push(<span key={i++} className="hl-kw">{t}</span>);
    else out.push(t);
  }
  return out;
}

