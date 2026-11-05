// Syntax highlighting for code cards (lazy chunk): highlight.js core + common languages.
// Output only contains <span class="hljs-…"> around escaped text.

import hljs from 'highlight.js/lib/core';
import bash from 'highlight.js/lib/languages/bash';
import c from 'highlight.js/lib/languages/c';
import cpp from 'highlight.js/lib/languages/cpp';
import csharp from 'highlight.js/lib/languages/csharp';
import css from 'highlight.js/lib/languages/css';
import diff from 'highlight.js/lib/languages/diff';
import dockerfile from 'highlight.js/lib/languages/dockerfile';
import go from 'highlight.js/lib/languages/go';
import ini from 'highlight.js/lib/languages/ini';
import java from 'highlight.js/lib/languages/java';
import javascript from 'highlight.js/lib/languages/javascript';
import json from 'highlight.js/lib/languages/json';
import kotlin from 'highlight.js/lib/languages/kotlin';
import makefile from 'highlight.js/lib/languages/makefile';
import markdown from 'highlight.js/lib/languages/markdown';
import php from 'highlight.js/lib/languages/php';
import python from 'highlight.js/lib/languages/python';
import ruby from 'highlight.js/lib/languages/ruby';
import rust from 'highlight.js/lib/languages/rust';
import scss from 'highlight.js/lib/languages/scss';
import sql from 'highlight.js/lib/languages/sql';
import swift from 'highlight.js/lib/languages/swift';
import typescript from 'highlight.js/lib/languages/typescript';
import xml from 'highlight.js/lib/languages/xml';
import yaml from 'highlight.js/lib/languages/yaml';

const LANGS = {
  bash, c, cpp, csharp, css, diff, dockerfile, go, ini, java, javascript, json, kotlin, makefile, markdown, php,
  python, ruby, rust, scss, sql, swift, typescript, xml, yaml,
};
for (const [name, def] of Object.entries(LANGS)) hljs.registerLanguage(name, def);

const BY_EXT: Record<string, string> = {
  sh: 'bash', bash: 'bash', zsh: 'bash', fish: 'bash', env: 'bash',
  c: 'c', h: 'c', cc: 'cpp', cpp: 'cpp', cxx: 'cpp', hpp: 'cpp', hh: 'cpp',
  cs: 'csharp', css: 'css', scss: 'scss', sass: 'scss', less: 'scss',
  diff: 'diff', patch: 'diff', go: 'go', mod: 'go',
  ini: 'ini', toml: 'ini', cfg: 'ini', conf: 'ini', properties: 'ini',
  java: 'java', js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'javascript',
  ts: 'typescript', tsx: 'typescript', mts: 'typescript', cts: 'typescript',
  json: 'json', jsonc: 'json', json5: 'json', lock: 'json',
  kt: 'kotlin', kts: 'kotlin', md: 'markdown', markdown: 'markdown', php: 'php',
  py: 'python', pyi: 'python', rb: 'ruby', rs: 'rust', sql: 'sql', swift: 'swift',
  html: 'xml', htm: 'xml', xml: 'xml', svg: 'xml', vue: 'xml', svelte: 'xml',
  yml: 'yaml', yaml: 'yaml',
};
const BY_NAME: Record<string, string> = { dockerfile: 'dockerfile', makefile: 'makefile', gnumakefile: 'makefile' };

export function languageFor(path: string): string | null {
  const base = path.slice(path.lastIndexOf('/') + 1).toLowerCase();
  if (BY_NAME[base]) return BY_NAME[base];
  if (base.startsWith('dockerfile')) return 'dockerfile';
  const i = base.lastIndexOf('.');
  return i > 0 ? (BY_EXT[base.slice(i + 1)] ?? null) : null;
}

/** Highlighted HTML (escaped) or null when the language is unknown / text too large. */
export function highlight(code: string, path: string): { html: string; language: string } | null {
  const language = languageFor(path);
  if (!language || code.length > 400_000) return null;
  try {
    return { html: hljs.highlight(code, { language, ignoreIllegals: true }).value, language };
  } catch {
    return null;
  }
}
