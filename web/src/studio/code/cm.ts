// CodeMirror 6 for the Code and Markdown studios (docs/API-studio.md §7). Loaded with
// the studios only. Theme colours come from the design tokens (CSS variables); language
// packs load on demand per file type; highlighting is off above 120 000 characters; no
// line wrapping. Line endings are kept: a CRLF file is edited with "\r\n" separators.

import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands';
import { bracketMatching, foldGutter, HighlightStyle, indentOnInput, StreamLanguage, syntaxHighlighting, type LanguageSupport, type StreamParser } from '@codemirror/language';
import { highlightSelectionMatches, search, searchKeymap } from '@codemirror/search';
import { Compartment, EditorState, type Extension } from '@codemirror/state';
import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers, placeholder as cmPlaceholder } from '@codemirror/view';
import { tags as t } from '@lezer/highlight';

export const HIGHLIGHT_LIMIT = 120_000;

const highlight = HighlightStyle.define([
  { tag: [t.comment, t.lineComment, t.blockComment, t.docComment], color: 'var(--syn-comment)', fontStyle: 'italic' },
  { tag: [t.keyword, t.modifier, t.controlKeyword, t.operatorKeyword, t.definitionKeyword, t.moduleKeyword], color: 'var(--syn-keyword)' },
  { tag: [t.string, t.special(t.string), t.regexp, t.character], color: 'var(--syn-string)' },
  { tag: [t.number, t.bool, t.null, t.atom], color: 'var(--syn-number)' },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.definition(t.function(t.variableName))], color: 'var(--syn-title)' },
  { tag: [t.typeName, t.className, t.namespace], color: 'var(--syn-type)' },
  { tag: [t.propertyName, t.attributeName], color: 'var(--syn-attr)' },
  { tag: [t.standard(t.variableName), t.self], color: 'var(--syn-builtin)' },
  { tag: [t.meta, t.annotation, t.processingInstruction], color: 'var(--syn-meta)' },
  { tag: [t.tagName], color: 'var(--syn-keyword)' },
  { tag: t.heading, color: 'var(--syn-title)', fontWeight: '700' },
  { tag: t.emphasis, fontStyle: 'italic' },
  { tag: t.strong, fontWeight: '700' },
  { tag: t.link, color: 'var(--syn-attr)', textDecoration: 'underline' },
  { tag: t.url, color: 'var(--syn-attr)' },
  { tag: t.monospace, color: 'var(--syn-string)' },
  { tag: t.invalid, color: 'var(--c-danger-300)' },
]);

const theme = EditorView.theme(
  {
    '&': { height: '100%', color: 'var(--syn-text)', backgroundColor: 'var(--surface-solid)', fontSize: 'var(--fs-sm)' },
    '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.55' },
    '.cm-content': { caretColor: 'var(--c-primary-300)', padding: 'var(--s-2) 0' },
    '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--c-primary-300)', borderLeftWidth: '2px' },
    '&.cm-focused': { outline: 'none' },
    '&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection': { backgroundColor: 'var(--editor-selection)' },
    '.cm-gutters': { backgroundColor: 'var(--surface-solid)', color: 'var(--text-3)', border: 'none', borderRight: 'var(--hairline) solid var(--glass-border)' },
    '.cm-activeLineGutter': { backgroundColor: 'var(--surface-hover)', color: 'var(--text-2)' },
    '.cm-activeLine': { backgroundColor: 'var(--editor-active-line)' },
    '.cm-selectionMatch': { backgroundColor: 'var(--editor-match)' },
    '.cm-matchingBracket': { backgroundColor: 'var(--editor-match)', outline: 'var(--hairline) solid var(--glass-border-strong)' },
    '.cm-searchMatch': { backgroundColor: 'var(--editor-search)', outline: 'var(--hairline) solid var(--c-warning)' },
    '.cm-searchMatch.cm-searchMatch-selected': { backgroundColor: 'var(--editor-search-current)' },
    '.cm-panels': { backgroundColor: 'var(--tile-card-bg)', color: 'var(--text)', borderTop: 'var(--hairline) solid var(--glass-border)' },
    '.cm-panels.cm-panels-top': { borderBottom: 'var(--hairline) solid var(--glass-border)' },
    '.cm-panel.cm-search': { padding: 'var(--s-2) var(--s-3)', fontFamily: 'var(--font-ui)', fontSize: 'var(--fs-sm)' },
    '.cm-panel.cm-search input, .cm-panel.cm-search button': { fontFamily: 'var(--font-ui)', fontSize: 'var(--fs-xs)' },
    '.cm-textfield': { backgroundColor: 'var(--surface-input)', color: 'var(--text)', border: 'var(--hairline) solid var(--glass-border)', borderRadius: 'var(--r-sm)', padding: 'var(--s-1) var(--s-2)' },
    '.cm-button': { backgroundImage: 'none', backgroundColor: 'var(--surface-input)', color: 'var(--text)', border: 'var(--hairline) solid var(--glass-border)', borderRadius: 'var(--r-sm)' },
    '.cm-panel.cm-search label': { color: 'var(--text-2)' },
    '.cm-foldPlaceholder': { backgroundColor: 'var(--surface-active)', border: 'none', color: 'var(--text-2)' },
    '.cm-placeholder': { color: 'var(--text-3)' },
  },
  { dark: true },
);

type Loader = () => Promise<LanguageSupport | Extension>;
const legacy = (load: () => Promise<StreamParser<unknown>>): Loader => async () => StreamLanguage.define(await load());

const BY_EXT: Record<string, { name: string; load: Loader }> = {};
function reg(exts: string[], name: string, load: Loader) {
  for (const e of exts) BY_EXT[e] = { name, load };
}
reg(['js', 'mjs', 'cjs'], 'JavaScript', async () => (await import('@codemirror/lang-javascript')).javascript());
reg(['jsx'], 'JSX', async () => (await import('@codemirror/lang-javascript')).javascript({ jsx: true }));
reg(['ts', 'mts', 'cts'], 'TypeScript', async () => (await import('@codemirror/lang-javascript')).javascript({ typescript: true }));
reg(['tsx'], 'TSX', async () => (await import('@codemirror/lang-javascript')).javascript({ jsx: true, typescript: true }));
reg(['py', 'pyw'], 'Python', async () => (await import('@codemirror/lang-python')).python());
reg(['go'], 'Go', async () => (await import('@codemirror/lang-go')).go());
reg(['rs'], 'Rust', async () => (await import('@codemirror/lang-rust')).rust());
reg(['java'], 'Java', async () => (await import('@codemirror/lang-java')).java());
reg(['c', 'h', 'cc', 'cpp', 'cxx', 'hpp', 'hh', 'hxx', 'm', 'mm'], 'C/C++', async () => (await import('@codemirror/lang-cpp')).cpp());
reg(['php'], 'PHP', async () => (await import('@codemirror/lang-php')).php());
reg(['html', 'htm', 'vue', 'svelte'], 'HTML', async () => (await import('@codemirror/lang-html')).html());
reg(['css', 'scss', 'less'], 'CSS', async () => (await import('@codemirror/lang-css')).css());
reg(['json', 'jsonc', 'json5'], 'JSON', async () => (await import('@codemirror/lang-json')).json());
reg(['yaml', 'yml'], 'YAML', async () => (await import('@codemirror/lang-yaml')).yaml());
reg(['xml', 'svg', 'plist', 'xsd'], 'XML', async () => (await import('@codemirror/lang-xml')).xml());
reg(['md', 'markdown', 'mdx'], 'Markdown', async () => (await import('@codemirror/lang-markdown')).markdown());
reg(['sql'], 'SQL', async () => (await import('@codemirror/lang-sql')).sql());
reg(['sh', 'bash', 'zsh', 'ksh', 'env'], 'Shell', legacy(async () => (await import('@codemirror/legacy-modes/mode/shell')).shell as StreamParser<unknown>));
reg(['rb', 'rake', 'gemspec'], 'Ruby', legacy(async () => (await import('@codemirror/legacy-modes/mode/ruby')).ruby as StreamParser<unknown>));
reg(['swift'], 'Swift', legacy(async () => (await import('@codemirror/legacy-modes/mode/swift')).swift as StreamParser<unknown>));
reg(['kt', 'kts'], 'Kotlin', legacy(async () => (await import('@codemirror/legacy-modes/mode/clike')).kotlin as StreamParser<unknown>));
reg(['cs'], 'C#', legacy(async () => (await import('@codemirror/legacy-modes/mode/clike')).csharp as StreamParser<unknown>));
reg(['toml'], 'TOML', legacy(async () => (await import('@codemirror/legacy-modes/mode/toml')).toml as StreamParser<unknown>));

const BY_NAME: Record<string, string> = { dockerfile: 'Shell', makefile: 'Shell', '.bashrc': 'Shell', '.zshrc': 'Shell', '.profile': 'Shell', '.env.example': 'Shell' };

export function languageOf(path: string): { name: string; load: Loader } | null {
  const base = path.split('/').pop()?.toLowerCase() ?? '';
  const byName = BY_NAME[base];
  if (byName) return Object.values(BY_EXT).find((l) => l.name === byName) ?? null;
  const i = base.lastIndexOf('.');
  return i > 0 ? (BY_EXT[base.slice(i + 1)] ?? null) : null;
}

export interface EditorHandle {
  view: EditorView;
  /** Replace the document (keeps undo history out of it). */
  setText(text: string): void;
  getText(): string;
  setReadOnly(ro: boolean): void;
  destroy(): void;
}

export interface EditorOptions {
  parent: HTMLElement;
  text: string;
  path: string;
  readOnly?: boolean;
  crlf?: boolean;
  placeholder?: string;
  wrap?: boolean;
  onChange?: (text: string) => void;
  onSave?: () => void;
}

/** Create an editor; the language loads in the background. */
export function createEditor(o: EditorOptions): EditorHandle {
  const lang = new Compartment();
  const ro = new Compartment();
  const big = o.text.length > HIGHLIGHT_LIMIT;
  const view = new EditorView({
    parent: o.parent,
    state: EditorState.create({
      doc: o.text,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        foldGutter(),
        history(),
        drawSelection(),
        highlightActiveLine(),
        indentOnInput(),
        bracketMatching(),
        highlightSelectionMatches(),
        search({ top: true }),
        big ? [] : syntaxHighlighting(highlight),
        theme,
        o.wrap ? EditorView.lineWrapping : [],
        o.crlf ? EditorState.lineSeparator.of('\r\n') : [],
        o.placeholder ? cmPlaceholder(o.placeholder) : [],
        keymap.of([
          { key: 'Mod-s', preventDefault: true, run: () => (o.onSave?.(), true) },
          ...searchKeymap,
          ...historyKeymap,
          indentWithTab,
          ...defaultKeymap,
        ]),
        ro.of([EditorState.readOnly.of(!!o.readOnly), EditorView.editable.of(!o.readOnly)]),
        lang.of([]),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) o.onChange?.(u.state.sliceDoc());
        }),
      ],
    }),
  });
  const l = languageOf(o.path);
  if (l && !big) {
    void l
      .load()
      .then((ext) => view.dispatch({ effects: lang.reconfigure(ext) }))
      .catch(() => undefined);
  }
  return {
    view,
    setText(text: string) {
      view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } });
    },
    getText: () => view.state.sliceDoc(),
    setReadOnly(r: boolean) {
      view.dispatch({ effects: ro.reconfigure([EditorState.readOnly.of(r), EditorView.editable.of(!r)]) });
    },
    destroy: () => view.destroy(),
  };
}
