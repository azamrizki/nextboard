// What the editor knows about a Laravel project (docs/API-studio.md §7.14): route, view, config,
// translation, env and component names complete, jump to where they are set, and say what they
// are. Everything comes from the hub's reading of the project's files; nothing runs for it.

import type { LaravelIndex, LaravelKey, LaravelRoute, LaravelView } from '../api';
import { monaco } from './monaco';
import { modelOwners } from './nativeExt';
import type { Workspace } from './workspace';

type Kind = 'route' | 'view' | 'config' | 'lang' | 'env' | 'component';

/** What the cursor is typing into: the text before it decides. The group is what is typed so far. */
const CONTEXTS: { re: RegExp; kind: Kind }[] = [
  { re: /(?:\broute|\bto_route|Route::has|->route)\(\s*['"]([^'"]*)$/, kind: 'route' },
  { re: /(?:\bview|View::make|->view)\(\s*['"]([^'"]*)$/, kind: 'view' },
  { re: /Route::view\(\s*['"][^'"]*['"]\s*,\s*['"]([^'"]*)$/, kind: 'view' },
  { re: /@(?:include|includeIf|includeFirst|extends|each|component)\(\s*['"]([^'"]*)$/, kind: 'view' },
  { re: /(?:\bconfig|Config::get|Config::string|Config::integer|Config::boolean)\(\s*['"]([^'"]*)$/, kind: 'config' },
  { re: /(?:\b__|\btrans|\btrans_choice|@lang|Lang::get)\(\s*['"]([^'"]*)$/, kind: 'lang' },
  { re: /\benv\(\s*['"]([^'"]*)$/, kind: 'env' },
  { re: /<x-([\w.:-]*)$/, kind: 'component' },
];

/** The same places, whole, to find what is under the cursor. */
const WHOLE: { re: RegExp; kind: Kind }[] = [
  { re: /(?:\broute|\bto_route|Route::has|->route)\(\s*['"]([^'"]+)['"]/g, kind: 'route' },
  { re: /(?:\bview|View::make|->view)\(\s*['"]([^'"]+)['"]/g, kind: 'view' },
  { re: /Route::view\(\s*['"][^'"]*['"]\s*,\s*['"]([^'"]+)['"]/g, kind: 'view' },
  { re: /@(?:include|includeIf|extends|each|component)\(\s*['"]([^'"]+)['"]/g, kind: 'view' },
  { re: /(?:\bconfig|Config::get|Config::string|Config::integer|Config::boolean)\(\s*['"]([^'"]+)['"]/g, kind: 'config' },
  { re: /(?:\b__|\btrans|\btrans_choice|@lang|Lang::get)\(\s*['"]([^'"]+)['"]/g, kind: 'lang' },
  { re: /<x-([\w.:-]+)/g, kind: 'component' },
];

function indexOf(model: monaco.editor.ITextModel): { ws: Workspace; idx: LaravelIndex } | null {
  const ws = modelOwners.get(model);
  const idx = ws?.laravel;
  return ws && idx?.detected ? { ws, idx } : null;
}

const under = (model: monaco.editor.ITextModel, pos: monaco.Position) => {
  const line = model.getLineContent(pos.lineNumber);
  for (const { re, kind } of WHOLE) {
    re.lastIndex = 0;
    for (const m of line.matchAll(re)) {
      const start = (m.index ?? 0) + m[0].lastIndexOf(m[1]) + 1;
      const end = start + m[1].length;
      if (pos.column >= start && pos.column <= end) return { kind, name: m[1], start, end };
    }
  }
  return null;
};

/** Where a project-relative file of the index really is. */
const fileOf = (ws: Workspace, idx: LaravelIndex, file: string) => ws.abs(idx.root ? `${idx.root}/${file}` : file);

const findRoute = (idx: LaravelIndex, name: string): LaravelRoute | undefined => idx.routes.find((r) => r.name === name);
const findKey = (list: LaravelKey[], key: string) => list.find((k) => k.key === key);
const findView = (list: LaravelView[], name: string) => list.find((v) => v.name === name);

let done = false;

/** The language and the providers, once: they find their project through the model. */
export function registerLaravel() {
  if (done) return;
  done = true;
  monaco.languages.register({ id: 'blade', aliases: ['Blade'], extensions: ['.blade.php'] });
  monaco.languages.setLanguageConfiguration('blade', {
    comments: { blockComment: ['{{--', '--}}'] },
    brackets: [
      ['{{', '}}'],
      ['{!!', '!!}'],
      ['(', ')'],
      ['[', ']'],
      ['{', '}'],
      ['<', '>'],
    ],
    autoClosingPairs: [
      { open: '{{', close: ' }}' },
      { open: '(', close: ')' },
      { open: '[', close: ']' },
      { open: "'", close: "'" },
      { open: '"', close: '"' },
    ],
  });
  monaco.languages.setMonarchTokensProvider('blade', {
    defaultToken: '',
    tokenizer: {
      root: [
        [/\{\{--/, 'comment', '@comment'],
        [/\{!!/, 'delimiter.php', '@raw'],
        [/\{\{/, 'delimiter.php', '@echo'],
        [/@@php\b/, 'keyword', '@php'],
        [/@@[a-zA-Z_]\w*/, 'keyword'],
        [/<!--/, 'comment', '@html'],
        [/<\/?[\w:.-]+/, 'tag', '@attrs'],
        [/[^<@{]+/, ''],
        [/[<@{]/, ''],
      ],
      comment: [
        [/--\}\}/, 'comment', '@pop'],
        [/./, 'comment'],
      ],
      html: [
        [/-->/, 'comment', '@pop'],
        [/./, 'comment'],
      ],
      attrs: [
        [/\/?>/, 'tag', '@pop'],
        [/\{\{/, 'delimiter.php', '@echo'],
        [/[:@]?[\w:.-]+(?==)/, 'attribute.name'],
        [/=/, 'delimiter'],
        [/"[^"]*"/, 'attribute.value'],
        [/'[^']*'/, 'attribute.value'],
        [/\s+/, ''],
        [/./, ''],
      ],
      echo: [[/\}\}/, 'delimiter.php', '@pop'], { include: '@expr' }],
      raw: [[/!!\}/, 'delimiter.php', '@pop'], { include: '@expr' }],
      php: [[/@@endphp\b/, 'keyword', '@pop'], { include: '@expr' }],
      expr: [
        [/\$\w+/, 'variable'],
        [/"[^"]*"/, 'string'],
        [/'[^']*'/, 'string'],
        [/\d+/, 'number'],
        [/[a-zA-Z_]\w*/, 'identifier'],
        [/\s+/, ''],
        [/./, ''],
      ],
    },
  } as monaco.languages.IMonarchLanguage);

  const selector = ['php', 'blade'];
  monaco.languages.registerCompletionItemProvider(selector, {
    triggerCharacters: ["'", '"', '.', '-'],
    provideCompletionItems(model, pos) {
      const found = indexOf(model);
      if (!found) return { suggestions: [] };
      const before = model.getLineContent(pos.lineNumber).slice(0, pos.column - 1);
      const ctx = CONTEXTS.map((c) => ({ kind: c.kind, m: c.re.exec(before) })).find((c) => c.m);
      if (!ctx?.m) return { suggestions: [] };
      const typed = ctx.m[1];
      const range = new monaco.Range(pos.lineNumber, pos.column - typed.length, pos.lineNumber, pos.column);
      const { idx } = found;
      const K = monaco.languages.CompletionItemKind;
      const item = (label: string, kind: monaco.languages.CompletionItemKind, detail: string) => ({ label, kind, detail, insertText: label, range, filterText: label });
      switch (ctx.kind) {
        case 'route':
          return { suggestions: idx.routes.filter((r) => r.name).map((r) => item(r.name!, K.Value, `${r.method} ${r.uri}`)) };
        case 'view':
          return { suggestions: idx.views.map((v) => item(v.name, K.File, v.file)) };
        case 'config':
          return { suggestions: idx.config.map((k) => item(k.key, K.Property, k.file)) };
        case 'lang':
          return { suggestions: idx.lang.map((k) => item(k.key, K.Text, k.file)) };
        case 'env':
          return { suggestions: idx.env.map((k) => item(k, K.Variable, '.env.example')) };
        case 'component':
          return { suggestions: idx.components.map((c) => item(c.name, K.Module, c.file)) };
      }
    },
  });

  monaco.languages.registerDefinitionProvider(selector, {
    provideDefinition(model, pos) {
      const found = indexOf(model);
      const at = found && under(model, pos);
      if (!found || !at) return null;
      const { ws, idx } = found;
      const go = (file: string, line = 1) => ({ uri: monaco.Uri.file(fileOf(ws, idx, file)), range: new monaco.Range(line, 1, line, 1) });
      switch (at.kind) {
        case 'route': {
          const r = findRoute(idx, at.name);
          return r?.file ? go(r.file, r.line) : null;
        }
        case 'view': {
          const v = findView(idx.views, at.name);
          return v ? go(v.file) : null;
        }
        case 'config': {
          const k = findKey(idx.config, at.name);
          return k ? go(k.file, k.line) : null;
        }
        case 'lang': {
          const k = findKey(idx.lang, at.name);
          return k ? go(k.file, k.line) : null;
        }
        case 'component': {
          const c = findView(idx.components, at.name);
          return c ? go(c.file) : null;
        }
      }
      return null;
    },
  });

  monaco.languages.registerHoverProvider(selector, {
    provideHover(model, pos) {
      const found = indexOf(model);
      const at = found && under(model, pos);
      if (!found || !at) return null;
      const { idx } = found;
      let text = '';
      if (at.kind === 'route') {
        const r = findRoute(idx, at.name);
        text = r ? `\`${r.method} /${r.uri.replace(/^\//, '')}\`${r.action ? ` · ${r.action}` : ''}` : 'No route of that name in `routes/`.';
      } else if (at.kind === 'view') {
        text = findView(idx.views, at.name)?.file ?? 'No view of that name in `resources/views`.';
      } else if (at.kind === 'config') {
        const k = findKey(idx.config, at.name);
        text = k ? `${k.file}:${k.line}` : 'Not found in `config/`.';
      } else if (at.kind === 'lang') {
        const k = findKey(idx.lang, at.name);
        if (k) text = `${k.file}:${k.line}`;
      } else if (at.kind === 'component') {
        text = findView(idx.components, at.name)?.file ?? 'No component of that name.';
      }
      if (!text) return null;
      return { range: new monaco.Range(pos.lineNumber, at.start, pos.lineNumber, at.end), contents: [{ value: text }] };
    },
  });
}
