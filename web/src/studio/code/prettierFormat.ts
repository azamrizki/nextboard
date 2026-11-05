// Prettier: the real formatter, running in the browser, as the formatter of the languages it
// knows while its extension is installed. It reads the project's JSON, YAML and TOML config and
// .editorconfig. A config written in JavaScript and third-party plugins are code from the
// project, and those do not run here.

import type { Options, Plugin } from 'prettier';
import { api, errorMessage } from '../../api';
import { toast } from '../../components/Toast';
import { fsApi } from '../api';
import { parseJsonc } from './jsonc';
import { monaco } from './monaco';
import { hasNative, modelOwners, onNativeChange } from './nativeExt';
import type { Workspace } from './workspace';

type Model = monaco.editor.ITextModel;
type Json = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any

const PLUGIN: Record<string, () => Promise<unknown>> = {
  babel: () => import('prettier/plugins/babel'),
  estree: () => import('prettier/plugins/estree'),
  typescript: () => import('prettier/plugins/typescript'),
  postcss: () => import('prettier/plugins/postcss'),
  html: () => import('prettier/plugins/html'),
  markdown: () => import('prettier/plugins/markdown'),
  yaml: () => import('prettier/plugins/yaml'),
  graphql: () => import('prettier/plugins/graphql'),
  glimmer: () => import('prettier/plugins/glimmer'),
};
const SCRIPT = ['babel', 'estree'];
const PAGE = ['html', 'postcss', 'babel', 'estree', 'typescript'];
const PARSER_PLUGINS: Record<string, string[]> = {
  babel: SCRIPT,
  json: SCRIPT,
  jsonc: SCRIPT,
  json5: SCRIPT,
  'json-stringify': SCRIPT,
  typescript: ['typescript', 'estree'],
  css: ['postcss'],
  scss: ['postcss'],
  less: ['postcss'],
  html: PAGE,
  vue: PAGE,
  markdown: ['markdown', 'yaml', ...PAGE],
  mdx: ['markdown', 'yaml', ...PAGE],
  yaml: ['yaml'],
  graphql: ['graphql'],
  glimmer: ['glimmer'],
};
const BY_NAME: Record<string, string> = { 'package.json': 'json-stringify', 'package-lock.json': 'json-stringify', 'composer.json': 'json-stringify', 'tsconfig.json': 'jsonc', 'jsconfig.json': 'jsonc', '.prettierrc': 'json' };
const BY_EXT: Record<string, string> = {
  js: 'babel',
  jsx: 'babel',
  mjs: 'babel',
  cjs: 'babel',
  ts: 'typescript',
  tsx: 'typescript',
  mts: 'typescript',
  cts: 'typescript',
  json: 'json',
  jsonc: 'jsonc',
  json5: 'json5',
  css: 'css',
  scss: 'scss',
  less: 'less',
  html: 'html',
  htm: 'html',
  vue: 'vue',
  md: 'markdown',
  markdown: 'markdown',
  mdx: 'mdx',
  yaml: 'yaml',
  yml: 'yaml',
  graphql: 'graphql',
  gql: 'graphql',
  hbs: 'glimmer',
  handlebars: 'glimmer',
};
const BY_LANGUAGE: Record<string, string> = {
  javascript: 'babel',
  typescript: 'typescript',
  json: 'json',
  jsonc: 'jsonc',
  css: 'css',
  scss: 'scss',
  less: 'less',
  html: 'html',
  vue: 'vue',
  markdown: 'markdown',
  mdx: 'mdx',
  yaml: 'yaml',
  graphql: 'graphql',
  handlebars: 'glimmer',
};

export function parserFor(name: string, language: string): string | undefined {
  const base = name.toLowerCase();
  return BY_NAME[base] ?? BY_EXT[base.slice(base.lastIndexOf('.') + 1)] ?? BY_LANGUAGE[language];
}

const CONFIG_DATA = ['.prettierrc', '.prettierrc.json', '.prettierrc.yaml', '.prettierrc.yml', '.prettierrc.json5', '.prettierrc.toml'];
const CONFIG_CODE = ['.prettierrc.js', '.prettierrc.cjs', '.prettierrc.mjs', '.prettierrc.ts', 'prettier.config.js', 'prettier.config.cjs', 'prettier.config.mjs', 'prettier.config.ts'];

/** One "key: value" or "key = value" per line, which is all these options need. */
export function parseFlat(text: string, sep: ':' | '='): Json {
  const out: Json = {};
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#') || /^\s/.test(raw)) continue;
    const i = line.indexOf(sep);
    if (i <= 0) continue;
    const key = line.slice(0, i).trim();
    const value = line.slice(i + 1).split(' #')[0].trim();
    if (value === 'true' || value === 'false') out[key] = value === 'true';
    else if (value !== '' && !Number.isNaN(Number(value))) out[key] = Number(value);
    else if (value) out[key] = value.replace(/^(["'])(.*)\1$/, '$2');
  }
  return out;
}

export function parseConfig(name: string, text: string): Json {
  if (name.endsWith('.toml')) return parseFlat(text, '=');
  if (name.endsWith('.yaml') || name.endsWith('.yml')) return parseFlat(text, ':');
  if (name === '.prettierrc' && !text.trimStart().startsWith('{')) return parseFlat(text, ':');
  return (parseJsonc(text) as Json) ?? {};
}

/** A glob as used by overrides, .editorconfig and .prettierignore. */
export function globToRegExp(glob: string): RegExp {
  let g = glob.replace(/\/$/, '');
  const anchored = g.includes('/');
  g = g.replace(/^\//, '');
  let re = '';
  let braces = 0;
  for (let i = 0; i < g.length; i++) {
    const c = g[i];
    if (c === '*') {
      if (g[i + 1] === '*') {
        i++;
        if (g[i + 1] === '/') {
          i++;
          re += '(?:.*/)?';
        } else re += '.*';
      } else re += '[^/]*';
    } else if (c === '?') re += '[^/]';
    else if (c === '{') {
      braces++;
      re += '(?:';
    } else if (c === '}' && braces) {
      braces--;
      re += ')';
    } else if (c === ',' && braces) re += '|';
    else re += c.replace(/[.+^$()|[\]\\]/g, '\\$&');
  }
  // without a slash a pattern matches at any depth, and a folder covers what is below it
  return new RegExp(`^${anchored ? '' : '(?:.*/)?'}${re}(?:/.*)?$`);
}

export function fromEditorConfig(text: string, rel: string): Json {
  const out: Json = {};
  let on = false;
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#') || line.startsWith(';')) continue;
    const head = /^\[(.*)\]$/.exec(line);
    if (head) {
      on = globToRegExp(head[1]).test(rel);
      continue;
    }
    if (!on) continue;
    const [k, v] = line.split('=').map((x) => x.trim().toLowerCase());
    if (k === 'indent_style') out.useTabs = v === 'tab';
    else if ((k === 'indent_size' || k === 'tab_width') && !Number.isNaN(Number(v))) out.tabWidth = Number(v);
    else if (k === 'max_line_length' && !Number.isNaN(Number(v))) out.printWidth = Number(v);
    else if (k === 'end_of_line' && ['lf', 'crlf', 'cr'].includes(v)) out.endOfLine = v;
  }
  return out;
}

export function ignoredBy(text: string, rel: string): boolean {
  let ignored = false;
  for (const raw of text.split('\n')) {
    const line = raw.trim();
    if (!line || line.startsWith('#')) continue;
    const negate = line.startsWith('!');
    if (globToRegExp(negate ? line.slice(1) : line).test(rel)) ignored = !negate;
  }
  return ignored;
}

const read = (ws: Workspace, path: string) => api.fsRead(ws.mid, path, 1 << 20).then((r) => r.content);

function parents(ws: Workspace, path: string): string[] {
  const root = ws.root.replace(/\/+$/, '');
  const out: string[] = [];
  let d = path.slice(0, path.lastIndexOf('/'));
  while (d && d.length >= root.length && d.startsWith(root)) {
    out.push(d);
    d = d.slice(0, d.lastIndexOf('/'));
  }
  return out;
}

/** The nearest configuration and .editorconfig for a file, with its overrides applied. */
async function optionsFor(ws: Workspace, path: string): Promise<Json> {
  const dirs = parents(ws, path);
  const names = [...CONFIG_DATA, ...CONFIG_CODE, 'package.json', '.editorconfig'];
  const stats = await fsApi.statBatch(ws.mid, dirs.flatMap((d) => names.map((n) => `${d}/${n}`))).catch(() => []);
  const found = new Set(stats.filter((s) => s.exists).map((s) => s.path));
  const has = (d: string, n: string) => found.has(`${d}/${n}`);
  let base: Json = {};
  const editorDir = dirs.find((d) => has(d, '.editorconfig'));
  if (editorDir) base = fromEditorConfig(await read(ws, `${editorDir}/.editorconfig`).catch(() => ''), path.slice(editorDir.length + 1));
  for (const d of dirs) {
    const rel = path.slice(d.length + 1);
    const name = CONFIG_DATA.find((n) => has(d, n));
    let config: Json | null = null;
    if (name) {
      config = parseConfig(name, await read(ws, `${d}/${name}`));
    } else if (CONFIG_CODE.some((n) => has(d, n))) {
      toast.info('Prettier: this project’s config is JavaScript, which does not run here. Using .editorconfig and the defaults.');
      return base;
    } else if (has(d, 'package.json')) {
      const pkg = parseJsonc(await read(ws, `${d}/package.json`).catch(() => '{}')) as Json | null;
      if (pkg && typeof pkg.prettier === 'object' && pkg.prettier) config = pkg.prettier as Json;
    }
    if (!config) continue;
    const { overrides, plugins: _plugins, $schema: _schema, ...own } = config;
    const merged: Json = { ...base, ...own };
    for (const o of Array.isArray(overrides) ? overrides : []) {
      const files: string[] = [o.files].flat();
      const skip: string[] = [o.excludeFiles ?? []].flat();
      if (files.some((f) => globToRegExp(f).test(rel)) && !skip.some((f) => globToRegExp(f).test(rel))) Object.assign(merged, o.options);
    }
    return merged;
  }
  return base;
}

async function isIgnored(ws: Workspace, path: string): Promise<boolean> {
  const text = await read(ws, `${ws.root.replace(/\/+$/, '')}/.prettierignore`).catch(() => '');
  return !!text && ignoredBy(text, ws.rel(path));
}

async function format(model: Model, range?: monaco.IRange): Promise<monaco.languages.TextEdit[]> {
  const ws = modelOwners.get(model);
  const path = model.uri.path;
  const name = path.slice(path.lastIndexOf('/') + 1);
  const parser = parserFor(name, model.getLanguageId());
  if (!ws || !parser) return [];
  try {
    if (await isIgnored(ws, path)) {
      toast.info(`Prettier: ${name} is in .prettierignore.`);
      return [];
    }
    const options = await optionsFor(ws, path);
    const chosen: string = typeof options.parser === 'string' ? options.parser : parser;
    const [prettier, ...plugins] = await Promise.all([import('prettier/standalone'), ...(PARSER_PLUGINS[chosen] ?? []).map((p) => PLUGIN[p]())]);
    const text = model.getValue();
    const opts: Options = { ...options, parser: chosen, filepath: name, plugins: plugins as Plugin[] };
    if (range) {
      opts.rangeStart = model.getOffsetAt({ lineNumber: range.startLineNumber, column: range.startColumn });
      opts.rangeEnd = model.getOffsetAt({ lineNumber: range.endLineNumber, column: range.endColumn });
    }
    const out = await prettier.format(text, opts);
    if (out === text) return [];
    return [{ range: model.getFullModelRange(), text: out }];
  } catch (err) {
    toast.error(`Prettier: ${errorMessage(err).split('\n')[0]}`);
    return [];
  }
}

let subs: monaco.IDisposable[] = [];

function sync() {
  const on = hasNative('prettier');
  if (on === subs.length > 0) return;
  subs.forEach((s) => s.dispose());
  subs = [];
  if (!on) return;
  for (const language of Object.keys(BY_LANGUAGE)) {
    // exclusive: it stands in for a language server's or the editor's own formatter
    const selector = { language, exclusive: true };
    subs.push(
      monaco.languages.registerDocumentFormattingEditProvider(selector, { displayName: 'Prettier', provideDocumentFormattingEdits: (model) => format(model) }),
      monaco.languages.registerDocumentRangeFormattingEditProvider(selector, { displayName: 'Prettier', provideDocumentRangeFormattingEdits: (model, range) => format(model, range) }),
    );
  }
}

onNativeChange(sync);
sync();
