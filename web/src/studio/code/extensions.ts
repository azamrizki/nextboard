// What an installed extension adds to the editor (docs/API-studio.md §7.6): colour and icon
// themes, snippets, language definitions and TextMate grammars. All of it is data read through
// the hub; extension code never runs, because Monaco has no extension host. For some extensions
// the editor does the main work itself, see nativeExt.ts.

import { prefs } from '../../lib/format';
import { extApi, type InstalledExtension } from '../api';
import { restoreIconTheme } from './iconTheme';
import { parseJsonc } from './jsonc';
import { BUILTIN_THEME, claimLanguage, monaco, setCurrentTheme } from './monaco';
import { setNativeList } from './nativeExt';

type Json = Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any

const LANGUAGE_ALIAS: Record<string, string> = { javascriptreact: 'javascript', typescriptreact: 'typescript', shellscript: 'shell', jsonc: 'json', 'objective-c': 'objective-c' };
const langId = (l: string) => LANGUAGE_ALIAS[l] ?? l;

/** A plist file as a plain value: old themes and grammars use them. */
function parsePlist(text: string): unknown {
  const doc = new DOMParser().parseFromString(text, 'text/xml');
  const value = (el: Element): unknown => {
    switch (el.tagName) {
      case 'dict': {
        const out: Json = {};
        const kids = [...el.children];
        for (let i = 0; i + 1 < kids.length; i += 2) out[kids[i].textContent ?? ''] = value(kids[i + 1]);
        return out;
      }
      case 'array':
        return [...el.children].map(value);
      case 'true':
        return true;
      case 'false':
        return false;
      case 'integer':
      case 'real':
        return Number(el.textContent);
      default:
        return el.textContent ?? '';
    }
  };
  const root = doc.querySelector('plist > *');
  if (!root) throw new Error('Not a plist file.');
  return value(root);
}

const parseFile = (text: string, path: string): Json => (/\.(json|jsonc|code-snippets)$/i.test(path) ? (parseJsonc(text) as Json) : (parsePlist(text) as Json));

const joinPath = (from: string, rel: string) => new URL(rel, `file:///${from}`).pathname.slice(1);

const hex = (c: unknown): string | null => {
  if (typeof c !== 'string' || !/^#([0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/i.test(c)) return null;
  return c;
};
/** Token colours: Monaco wants six digits, without "#". */
const tokenHex = (c: unknown): string | null => {
  const h = hex(c);
  if (!h) return null;
  const v = h.slice(1);
  return v.length <= 4 ? [...v.slice(0, 3)].map((x) => x + x).join('') : v.slice(0, 6);
};

interface RawTheme {
  colors: Record<string, string>;
  tokenColors: Json[];
}

async function loadTheme(id: string, path: string, depth = 0): Promise<RawTheme> {
  const data = parseFile(await extApi.file(id, path), path);
  const out: RawTheme = { colors: {}, tokenColors: [] };
  if (typeof data.include === 'string' && depth < 8) {
    const base = await loadTheme(id, joinPath(path, data.include), depth + 1);
    out.colors = base.colors;
    out.tokenColors = base.tokenColors;
  }
  Object.assign(out.colors, data.colors ?? {});
  let own = data.tokenColors ?? data.settings ?? [];
  if (typeof own === 'string') {
    const inner = parseFile(await extApi.file(id, joinPath(path, own)), own);
    own = inner.settings ?? inner.tokenColors ?? [];
  }
  out.tokenColors = [...out.tokenColors, ...(Array.isArray(own) ? own : [])];
  return out;
}

/** Monaco's built-in languages use short tokens: point them at the theme's TextMate scopes. */
const MONARCH_TOKENS: [string, string[]][] = [
  ['comment', ['comment']],
  ['string', ['string']],
  ['number', ['constant.numeric', 'constant']],
  ['keyword', ['keyword.control', 'keyword', 'storage.type', 'storage']],
  ['type', ['entity.name.type', 'support.type', 'entity.name.class', 'storage.type']],
  ['type.identifier', ['entity.name.type', 'entity.name.class', 'support.class']],
  ['identifier.function', ['entity.name.function', 'support.function']],
  ['function', ['entity.name.function', 'support.function']],
  ['variable', ['variable.other', 'variable']],
  ['constant', ['constant.language', 'constant']],
  ['regexp', ['string.regexp']],
  ['tag', ['entity.name.tag']],
  ['attribute.name', ['entity.other.attribute-name']],
  ['attribute.value', ['string']],
  ['delimiter', ['punctuation']],
  ['annotation', ['meta.decorator', 'entity.name.function.decorator']],
  ['metatag', ['meta.tag', 'punctuation.definition.tag']],
  ['operator', ['keyword.operator']],
];

const themeId = (extId: string, path: string) => `ext-${extId}-${path}`.replace(/[^a-z0-9-]/gi, '-').toLowerCase();

function defineTheme(id: string, uiTheme: string, raw: RawTheme) {
  const rules: monaco.editor.ITokenThemeRule[] = [];
  const byScope = new Map<string, monaco.editor.ITokenThemeRule>();
  for (const t of raw.tokenColors) {
    const fg = tokenHex(t.settings?.foreground);
    const fontStyle = typeof t.settings?.fontStyle === 'string' ? t.settings.fontStyle : undefined;
    if (!t.scope) {
      if (fg && !raw.colors['editor.foreground']) raw.colors['editor.foreground'] = `#${fg}`;
      continue;
    }
    const scopes: string[] = Array.isArray(t.scope) ? t.scope : String(t.scope).split(',');
    for (const s of scopes.map((x) => x.trim()).filter(Boolean)) {
      // only plain scopes: Monaco has no descendant selectors
      if (/\s/.test(s)) continue;
      const rule: monaco.editor.ITokenThemeRule = { token: s };
      if (fg) rule.foreground = fg;
      if (fontStyle !== undefined) rule.fontStyle = fontStyle;
      rules.push(rule);
      byScope.set(s, rule);
    }
  }
  for (const [token, scopes] of MONARCH_TOKENS) {
    const found = scopes.map((s) => byScope.get(s)).find(Boolean);
    if (found && !byScope.has(token)) rules.push({ ...found, token });
  }
  const colors: Record<string, string> = {};
  for (const [k, v] of Object.entries(raw.colors)) {
    const c = hex(v);
    if (c) colors[k] = c;
  }
  const base: monaco.editor.BuiltinTheme = uiTheme === 'vs' ? 'vs' : uiTheme === 'hc-black' ? 'hc-black' : uiTheme === 'hc-light' ? 'hc-light' : 'vs-dark';
  monaco.editor.defineTheme(id, { base, inherit: true, rules, colors });
}

export async function applyTheme(ext: InstalledExtension, path: string) {
  const theme = ext.contributes.themes?.find((t) => t.path === path);
  const raw = await loadTheme(ext.id, path);
  const id = themeId(ext.id, path);
  defineTheme(id, theme?.uiTheme ?? 'vs-dark', raw);
  setCurrentTheme(id);
  prefs.set('wb.theme', JSON.stringify({ ext: ext.id, path }));
}

export function useBuiltinTheme(id: string = BUILTIN_THEME) {
  setCurrentTheme(id);
  prefs.set('wb.theme', id === BUILTIN_THEME ? '' : JSON.stringify({ builtin: id }));
}

export const savedThemeKey = (): string => prefs.get('wb.theme') ?? '';

// ---- snippets, languages, grammars --------------------------------------------------

const applied = new Map<string, monaco.IDisposable[]>();

async function addSnippets(ext: InstalledExtension, subs: monaco.IDisposable[]) {
  for (const s of ext.contributes.snippets ?? []) {
    let data: Json;
    try {
      data = parseFile(await extApi.file(ext.id, s.path), s.path);
    } catch {
      continue;
    }
    const entries = Object.entries(data).filter(([, v]) => v && typeof v === 'object' && v.body !== undefined);
    subs.push(
      monaco.languages.registerCompletionItemProvider(langId(s.language), {
        provideCompletionItems(model, position) {
          const word = model.getWordUntilPosition(position);
          const range = new monaco.Range(position.lineNumber, word.startColumn, position.lineNumber, word.endColumn);
          const suggestions: monaco.languages.CompletionItem[] = [];
          for (const [name, v] of entries as [string, Json][]) {
            const body = Array.isArray(v.body) ? v.body.join('\n') : String(v.body);
            for (const prefix of Array.isArray(v.prefix) ? v.prefix : [v.prefix ?? name]) {
              suggestions.push({
                label: { label: String(prefix), description: name },
                kind: monaco.languages.CompletionItemKind.Snippet,
                insertText: body,
                insertTextRules: monaco.languages.CompletionItemInsertTextRule.InsertAsSnippet,
                documentation: typeof v.description === 'string' ? v.description : Array.isArray(v.description) ? v.description.join('\n') : undefined,
                detail: `Snippet from ${ext.displayName || ext.name}`,
                range,
              });
            }
          }
          return { suggestions };
        },
      }),
    );
  }
}

const toRegex = (v: unknown): RegExp | undefined => {
  const src = typeof v === 'string' ? v : (v as Json | undefined)?.pattern;
  if (typeof src !== 'string') return undefined;
  try {
    return new RegExp(src, (v as Json)?.flags);
  } catch {
    return undefined;
  }
};

/** Models change language only once the grammar is ready, so that a file never flashes uncoloured. */
const pendingClaims: (() => void)[] = [];

async function addLanguages(ext: InstalledExtension) {
  for (const l of ext.contributes.languages ?? []) {
    if (!l.id) continue;
    monaco.languages.register({ id: l.id, aliases: l.aliases ?? undefined, extensions: l.extensions ?? undefined, filenames: l.filenames ?? undefined });
    pendingClaims.push(() => claimLanguage(l.id, l.extensions ?? [], l.filenames ?? []));
    if (!l.configuration) continue;
    try {
      const c = parseFile(await extApi.file(ext.id, l.configuration), l.configuration);
      monaco.languages.setLanguageConfiguration(l.id, {
        comments: c.comments,
        brackets: c.brackets,
        autoClosingPairs: c.autoClosingPairs?.map((p: Json | string[]) => (Array.isArray(p) ? { open: p[0], close: p[1] } : p)),
        surroundingPairs: c.surroundingPairs?.map((p: Json | string[]) => (Array.isArray(p) ? { open: p[0], close: p[1] } : p)),
        wordPattern: toRegex(c.wordPattern),
        indentationRules: c.indentationRules?.increaseIndentPattern && c.indentationRules?.decreaseIndentPattern ? { increaseIndentPattern: toRegex(c.indentationRules.increaseIndentPattern)!, decreaseIndentPattern: toRegex(c.indentationRules.decreaseIndentPattern)! } : undefined,
      });
    } catch {
      /* the language still works without its configuration */
    }
  }
}

/** Load every extension's contributions, and the saved theme. */
export async function loadExtensions(): Promise<InstalledExtension[]> {
  let list: InstalledExtension[];
  try {
    list = (await extApi.list()).items ?? [];
  } catch {
    return [];
  }
  await Promise.all(list.map((x) => applyExtension(x)));
  await refreshGrammars(list);
  await restoreIconTheme(list);
  const saved = savedThemeKey();
  if (saved) {
    try {
      const v = JSON.parse(saved) as { ext?: string; path?: string; builtin?: string };
      if (v.builtin) setCurrentTheme(v.builtin);
      const ext = list.find((x) => x.id === v.ext);
      if (ext && v.path) await applyTheme(ext, v.path);
    } catch {
      /* the default theme stays */
    }
  }
  return list;
}

export async function applyExtension(ext: InstalledExtension) {
  removeExtension(ext.id);
  const subs: monaco.IDisposable[] = [];
  applied.set(ext.id, subs);
  await addLanguages(ext);
  await addSnippets(ext, subs);
}

export function removeExtension(id: string) {
  for (const s of applied.get(id) ?? []) s.dispose();
  applied.delete(id);
}

let grammarSubs: monaco.IDisposable[] = [];

/** TextMate grammars, in the worker-free way Monaco's tokenizer can call. */
export async function refreshGrammars(list: InstalledExtension[]) {
  setNativeList(list);
  for (const s of grammarSubs) s.dispose();
  grammarSubs = [];
  const grammars = list.flatMap((x) => (x.contributes.grammars ?? []).map((g) => ({ ...g, ext: x.id })));
  if (!grammars.length) {
    for (const claim of pendingClaims.splice(0)) claim();
    return;
  }
  const { tokensProviderFor } = await import('./textmate');
  for (const g of grammars) {
    if (!g.language) continue; // injections and embedded-only grammars
    const provider = await tokensProviderFor(g.scopeName, async (scope) => {
      const hit = grammars.find((x) => x.scopeName === scope);
      if (!hit) return null;
      return { text: await extApi.file(hit.ext, hit.path), path: hit.path };
    }).catch(() => null);
    if (provider) grammarSubs.push(monaco.languages.setTokensProvider(g.language, provider));
  }
  for (const claim of pendingClaims.splice(0)) claim();
}
