// Monaco, the editor of VS Code, for the Code workbench (docs/API-studio.md §7). Loaded
// with the workbench chunk only and bundled locally: the hub works offline. Colours come
// from the design tokens; the language of a model comes from its file name.

import * as monaco from 'monaco-editor';
import { currentAppTheme, onThemeChange } from '../../lib/theme';
import EditorWorker from 'monaco-editor/editor/editor.worker.js?worker';
import CssWorker from 'monaco-editor/language/css/css.worker.js?worker';
import HtmlWorker from 'monaco-editor/language/html/html.worker.js?worker';
import JsonWorker from 'monaco-editor/language/json/json.worker.js?worker';
import TsWorker from 'monaco-editor/language/typescript/ts.worker.js?worker';

export { monaco };

self.MonacoEnvironment = {
  getWorker(_id, label) {
    if (label === 'json') return new JsonWorker();
    if (label === 'css' || label === 'scss' || label === 'less') return new CssWorker();
    if (label === 'html' || label === 'handlebars' || label === 'razor') return new HtmlWorker();
    if (label === 'typescript' || label === 'javascript') return new TsWorker();
    return new EditorWorker();
  },
};

/** A CSS custom property as a colour Monaco accepts (#rrggbb or #rrggbbaa). */
function tokenColor(name: string, fallback: string): string {
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  // The production build minifies a token to #fff; Monaco throws on anything but the long form.
  if (/^#[0-9a-f]{3,4}$/i.test(v)) return `#${[...v.slice(1)].map((c) => c + c).join('')}`;
  if (/^#([0-9a-f]{6}|[0-9a-f]{8})$/i.test(v)) return v;
  const m = /^rgba?\(\s*(\d+)[,\s]+(\d+)[,\s]+(\d+)(?:[,/\s]+([\d.]+))?\s*\)$/.exec(v);
  if (!m) return fallback;
  const hex = (n: number) => Math.round(n).toString(16).padStart(2, '0');
  const alpha = m[4] === undefined ? '' : hex(Number(m[4]) * 255);
  return `#${hex(+m[1])}${hex(+m[2])}${hex(+m[3])}${alpha}`;
}

const bare = (c: string) => c.replace(/^#/, '').slice(0, 6);

export const BUILTIN_THEME = 'nextboard';
let theme = BUILTIN_THEME;

/** The theme every editor uses; installed extensions can change it. */
export const FONT = '"JetBrains Mono Variable", "JetBrains Mono", ui-monospace, Menlo, Consolas, monospace';

export const currentTheme = () => theme;
export function setCurrentTheme(id: string) {
  theme = id;
  monaco.editor.setTheme(id);
}

let ready = false;

/** The NextBoard theme from the tokens of the app's light or dark mode. */
function defineBuiltinTheme() {
  const syn = (name: string, fallback: string) => bare(tokenColor(name, fallback));
  const surface = tokenColor('--surface-solid', '#0b1020');
  const card = tokenColor('--tile-card-bg', '#151c32');
  const text = tokenColor('--syn-text', '#e6eaf2');
  const text2 = tokenColor('--text-2', '#b4bccd');
  const text3 = tokenColor('--text-3', '#8790a3');
  const primary = tokenColor('--c-primary-300', '#6e95ff');
  const border = tokenColor('--glass-border', '#ffffff17');
  monaco.editor.defineTheme(BUILTIN_THEME, {
    base: currentAppTheme() === 'light' ? 'vs' : 'vs-dark',
    inherit: true,
    rules: [
      { token: '', foreground: bare(text) },
      { token: 'comment', foreground: syn('--syn-comment', '#7d88a1'), fontStyle: 'italic' },
      { token: 'keyword', foreground: syn('--syn-keyword', '#b49cff') },
      { token: 'string', foreground: syn('--syn-string', '#6bf0b0') },
      { token: 'number', foreground: syn('--syn-number', '#f2b27a') },
      { token: 'regexp', foreground: syn('--syn-string', '#6bf0b0') },
      { token: 'type', foreground: syn('--syn-type', '#8be9f5') },
      { token: 'type.identifier', foreground: syn('--syn-type', '#8be9f5') },
      { token: 'identifier.function', foreground: syn('--syn-title', '#9db8ff') },
      { token: 'function', foreground: syn('--syn-title', '#9db8ff') },
      { token: 'attribute.name', foreground: syn('--syn-attr', '#4fd6e8') },
      { token: 'attribute.value', foreground: syn('--syn-string', '#6bf0b0') },
      { token: 'tag', foreground: syn('--syn-keyword', '#b49cff') },
      { token: 'metatag', foreground: syn('--syn-meta', '#ff8a9c') },
      { token: 'annotation', foreground: syn('--syn-meta', '#ff8a9c') },
      { token: 'delimiter', foreground: bare(text2) },
    ],
    colors: {
      'editor.background': surface,
      'editor.foreground': text,
      'editorCursor.foreground': primary,
      'editorLineNumber.foreground': text3,
      'editorLineNumber.activeForeground': text2,
      'editor.selectionBackground': tokenColor('--editor-selection', '#6e95ff4d'),
      'editor.lineHighlightBackground': tokenColor('--editor-active-line', '#ffffff09'),
      'editor.lineHighlightBorder': '#00000000',
      'editor.findMatchBackground': tokenColor('--editor-search-current', '#e8955880'),
      'editor.findMatchHighlightBackground': tokenColor('--editor-search', '#e8955840'),
      'editor.wordHighlightBackground': tokenColor('--editor-match', '#6e95ff2e'),
      'editorGutter.background': surface,
      'editorWidget.background': card,
      'editorWidget.border': border,
      'editorSuggestWidget.background': card,
      'editorSuggestWidget.border': border,
      'editorHoverWidget.background': card,
      'editorHoverWidget.border': border,
      'quickInput.background': card,
      'input.background': surface,
      'focusBorder': primary,
      'minimap.background': surface,
      'scrollbarSlider.background': tokenColor('--surface-hover', '#ffffff14'),
      'scrollbarSlider.hoverBackground': tokenColor('--surface-active', '#ffffff24'),
      'scrollbarSlider.activeBackground': tokenColor('--tile-outline', '#ffffff33'),
    },
  });
}

/** Theme and language defaults; safe to call more than once. */
export function setupMonaco() {
  if (ready) return;
  ready = true;
  defineBuiltinTheme();
  monaco.editor.setTheme(theme);
  // a theme picked from an extension stays; the built-in one follows the app
  onThemeChange(() => {
    defineBuiltinTheme();
    if (theme === BUILTIN_THEME) monaco.editor.setTheme(theme);
  });
  // Monaco rejects its own pending work when an editor goes away: not an error
  window.addEventListener('unhandledrejection', (e) => {
    if ((e.reason as { name?: string } | null)?.name === 'Canceled') e.preventDefault();
  });
  // The real font arrives later than the first measurement.
  void document.fonts?.ready.then(() => monaco.editor.remeasureFonts());
  // Without the project's types every import would be an error: syntax only, like the
  // CSS/HTML/JSON services. A language server replaces all of it (see lsp.ts).
  const diagnostics = { noSemanticValidation: true, noSyntaxValidation: false };
  const compiler = {
    allowJs: true,
    allowNonTsExtensions: true,
    jsx: monaco.typescript.JsxEmit.Preserve,
    target: monaco.typescript.ScriptTarget.ESNext,
    module: monaco.typescript.ModuleKind.ESNext,
    moduleResolution: monaco.typescript.ModuleResolutionKind.NodeJs,
  };
  for (const d of [monaco.typescript.typescriptDefaults, monaco.typescript.javascriptDefaults]) {
    d.setDiagnosticsOptions(diagnostics);
    d.setCompilerOptions(compiler);
  }
}

/** Hand TypeScript and JavaScript to a language server: the built-in service steps aside. */
export function disableBuiltinTypescript() {
  const off = {
    completionItems: false,
    hovers: false,
    documentSymbols: false,
    definitions: false,
    references: false,
    documentHighlights: false,
    rename: false,
    diagnostics: false,
    documentRangeFormattingEdits: false,
    signatureHelp: false,
    onTypeFormattingEdits: false,
    codeActions: false,
    inlayHints: false,
  };
  monaco.typescript.typescriptDefaults.setModeConfiguration(off);
  monaco.typescript.javascriptDefaults.setModeConfiguration(off);
}

const BY_NAME: Record<string, string> = {
  dockerfile: 'dockerfile',
  makefile: 'shell',
  gnumakefile: 'shell',
  '.bashrc': 'shell',
  '.zshrc': 'shell',
  '.profile': 'shell',
  '.env': 'shell',
  '.env.example': 'shell',
  '.env.local': 'shell',
  '.gitconfig': 'ini',
  '.editorconfig': 'ini',
  '.npmrc': 'ini',
  'go.mod': 'plaintext',
  'go.sum': 'plaintext',
};
const BY_EXT: Record<string, string> = {
  vue: 'html',
  svelte: 'html',
  toml: 'ini',
  env: 'shell',
  zsh: 'shell',
  mts: 'typescript',
  cts: 'typescript',
  jsonc: 'json',
  json5: 'json',
  mdx: 'markdown',
  gradle: 'kotlin',
  kts: 'kotlin',
  h: 'cpp',
};

/** File extensions and names an installed extension declared: they beat the guesses below. */
const claimed = new Map<string, string>();

export function claimLanguage(id: string, extensions: string[], filenames: string[]) {
  for (const e of extensions) claimed.set(e.toLowerCase(), id);
  for (const f of filenames) claimed.set(f.toLowerCase(), id);
  // files already open come over to the new language
  for (const m of monaco.editor.getModels()) {
    if (m.uri.scheme !== 'file') continue;
    const lang = languageFor(m.uri.path);
    if (lang && lang !== m.getLanguageId()) monaco.editor.setModelLanguage(m, lang);
  }
}

/** Language id for a path, or undefined to let Monaco decide from the extension. */
export function languageFor(path: string): string | undefined {
  const base = path.split('/').pop()?.toLowerCase() ?? '';
  const named = claimed.get(base);
  if (named) return named;
  for (let i = base.indexOf('.'); i >= 0; i = base.indexOf('.', i + 1)) {
    const hit = claimed.get(base.slice(i));
    if (hit) return hit;
  }
  if (base.endsWith('.blade.php')) return 'blade';
  if (BY_NAME[base]) return BY_NAME[base];
  const i = base.lastIndexOf('.');
  return i > 0 ? BY_EXT[base.slice(i + 1)] : undefined;
}

/** Display name of a language id ("Plain Text" for an unknown one). */
export function languageLabel(id: string): string {
  return monaco.languages.getLanguages().find((l) => l.id === id)?.aliases?.[0] ?? (id === 'plaintext' ? 'Plain Text' : id);
}
