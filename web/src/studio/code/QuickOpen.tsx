// Go to file (Ctrl+P) and the command palette (Ctrl+Shift+P or F1), one input as in VS
// Code: no prefix finds files with fuzzy matching and shows recent ones first, ">" runs
// commands, ":" goes to a line, "@" lists the file's symbols, "#" searches symbols across
// the project. "name.go:12" opens a file at a line. Ctrl+Enter opens to the side.

import { useDeferredValue, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import { errorMessage } from '../../api';
import { Icon, type IconName } from '../../components/Icon';
import { fuzzyMatch, fuzzyTop } from './fuzzy';
import { FileIcon } from './Explorer';
import { nameOf, type Workspace } from './workspace';

export interface Command {
  id: string;
  label: string;
  keys?: string;
  icon?: IconName;
  run: () => void;
}

export interface PickItem {
  label: string;
  detail?: string;
  run: () => void;
}

interface Item {
  key: string;
  label: ReactNode;
  detail?: ReactNode;
  keys?: string;
  icon?: IconName;
  file?: string;
  run: (side: boolean) => void;
}

interface Props {
  ws: Workspace;
  initial: string;
  commands: () => Command[];
  /** Replaces the list with these choices (language, indentation). */
  pick?: { placeholder: string; items: PickItem[] };
  onClose: () => void;
}

function highlight(text: string, positions: number[]): ReactNode {
  if (!positions.length) return text;
  const set = new Set(positions);
  const out: ReactNode[] = [];
  let run = '';
  let inMatch = false;
  for (let i = 0; i <= text.length; i++) {
    const m = set.has(i);
    if (i === text.length || m !== inMatch) {
      if (run) out.push(inMatch ? <mark key={i}>{run}</mark> : run);
      run = '';
      inMatch = m;
    }
    if (i < text.length) run += text[i];
  }
  return out;
}

export function QuickOpen({ ws, initial, commands, pick, onClose }: Props) {
  const [value, setValue] = useState(initial);
  const [active, setActive] = useState(0);
  const [files, setFiles] = useState<string[] | null>(null);
  const [filesNote, setFilesNote] = useState('');
  const [symbols, setSymbols] = useState<Awaited<ReturnType<Workspace['lsp']['workspaceSymbols']>>>([]);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const prevFocus = useRef(document.activeElement as HTMLElement | null);
  const deferred = useDeferredValue(value);
  const cmds = useMemo(() => (value.startsWith('>') ? commands() : []), [value.startsWith('>')]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (pick) return;
    ws.fileList()
      .then((l) => {
        setFiles(l.items ?? []);
        if (l.truncated) setFilesNote('The list is cut short in this very large project.');
      })
      .catch((err) => {
        setFiles([]);
        setFilesNote(errorMessage(err));
      });
  }, [ws, pick]);

  useEffect(() => {
    if (!value.startsWith('#')) return;
    const q = value.slice(1).trim();
    if (!q) {
      setSymbols([]);
      return;
    }
    const t = window.setTimeout(() => {
      void ws.lsp.workspaceSymbols(q).then(setSymbols);
    }, 200);
    return () => window.clearTimeout(t);
  }, [value, ws]);

  useEffect(() => {
    const el = inputRef.current;
    el?.focus();
    el?.setSelectionRange(el.value.length, el.value.length);
    const back = prevFocus.current;
    return () => back?.focus?.();
  }, []);

  useEffect(() => setActive(0), [deferred]);

  const items: Item[] = useMemo(() => {
    const q = deferred;
    if (pick) {
      const hits = pick.items
        .map((it) => ({ it, hit: fuzzyMatch(q, it.label) }))
        .filter((x) => x.hit)
        .sort((a, b) => (b.hit?.score ?? 0) - (a.hit?.score ?? 0));
      return hits.slice(0, 200).map(({ it, hit }) => ({ key: it.label, label: highlight(it.label, hit?.positions ?? []), detail: it.detail, run: () => it.run() }));
    }
    if (q.startsWith('>')) {
      const term = q.slice(1).trim();
      const hits = cmds
        .map((c) => ({ c, hit: fuzzyMatch(term, c.label) }))
        .filter((x) => x.hit)
        .sort((a, b) => (term ? (b.hit?.score ?? 0) - (a.hit?.score ?? 0) : a.c.label.localeCompare(b.c.label)));
      return hits.slice(0, 300).map(({ c, hit }) => ({ key: c.id, label: highlight(c.label, hit?.positions ?? []), keys: c.keys, icon: c.icon, run: () => c.run() }));
    }
    if (q.startsWith(':')) {
      const [line, col] = q.slice(1).split(/[:,]/).map((n) => Number(n));
      const doc = ws.activeDoc;
      if (!doc || !line) return [{ key: 'line', label: 'Type a line number to go to', run: () => undefined }];
      return [{ key: 'line', label: `Go to line ${line}${col ? `, column ${col}` : ''}`, run: () => ws.open(doc.path, { line, col: col || 1 }) }];
    }
    if (q.startsWith('@')) {
      return [
        {
          key: 'symbols',
          label: 'Show the symbols of this file',
          keys: 'Enter',
          run: () => {
            const ed = ws.activeEditor();
            ed?.focus();
            void ed?.getAction('editor.action.quickOutline')?.run();
          },
        },
      ];
    }
    if (q.startsWith('#')) {
      return symbols.map((s) => ({
        key: `${s.path}:${s.line}:${s.name}`,
        label: s.name,
        detail: `${s.detail ? `${s.detail} · ` : ''}${ws.rel(s.path)}`,
        icon: 'braces' as IconName,
        run: (side: boolean) => ws.open(s.path, { line: s.line, col: s.col, side, source: true }),
      }));
    }
    const list = files ?? [];
    const m = /^(.*?):(\d+)(?::(\d+))?$/.exec(q.trim());
    const term = m ? m[1] : q.trim();
    const line = m ? Number(m[2]) : undefined;
    const col = m?.[3] ? Number(m[3]) : undefined;
    const fileItem = (rel: string, positions: number[] = []): Item => {
      const base = rel.lastIndexOf('/') + 1;
      return {
        key: rel,
        label: highlight(rel.slice(base), positions.filter((p) => p >= base).map((p) => p - base)),
        detail: base ? highlight(rel.slice(0, base - 1), positions.filter((p) => p < base - 1)) : undefined,
        file: nameOf(rel),
        run: (side: boolean) => ws.open(ws.abs(rel), { pin: true, side, line, col, source: !!line }),
      };
    };
    if (!term) {
      const recent = ws.recent.filter((p) => ws.inside(p)).map((p) => ws.rel(p));
      const seen = new Set(recent);
      return [...recent, ...list.filter((r) => !seen.has(r)).slice(0, 50)].slice(0, 60).map((r) => fileItem(r));
    }
    return fuzzyTop(term, list, 60).map(({ path, hit }) => fileItem(path, hit.positions));
  }, [deferred, files, cmds, symbols, pick, ws]);

  const choose = (i: number, side: boolean) => {
    const it = items[i];
    if (!it) return;
    onClose();
    // after the palette has given the focus back
    window.setTimeout(() => it.run(side), 0);
  };

  useEffect(() => {
    listRef.current?.querySelector('.is-active')?.scrollIntoView({ block: 'nearest' });
  }, [active]);

  const placeholder = pick?.placeholder ?? 'Go to file. Type > for commands, : for a line, @ for symbols, # across the project';

  return createPortal(
    <div
      className="wb-palette-layer modal-layer"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
      onKeyDown={(e) => e.stopPropagation()}
      onClick={(e) => e.stopPropagation()}
    >
      <div className="wb-palette" role="dialog" aria-label={pick ? pick.placeholder : 'Go to file'}>
        <input
          ref={inputRef}
          className="wb-palette-input"
          role="combobox"
          aria-expanded="true"
          aria-controls="wb-palette-list"
          aria-activedescendant={items[active] ? `wb-pal-${active}` : undefined}
          spellCheck={false}
          placeholder={placeholder}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape') {
              e.preventDefault();
              onClose();
            } else if (e.key === 'ArrowDown') {
              e.preventDefault();
              setActive((a) => Math.min(a + 1, items.length - 1));
            } else if (e.key === 'ArrowUp') {
              e.preventDefault();
              setActive((a) => Math.max(a - 1, 0));
            } else if (e.key === 'PageDown') {
              e.preventDefault();
              setActive((a) => Math.min(a + 10, items.length - 1));
            } else if (e.key === 'PageUp') {
              e.preventDefault();
              setActive((a) => Math.max(a - 10, 0));
            } else if (e.key === 'Enter') {
              e.preventDefault();
              choose(active, e.ctrlKey || e.metaKey);
            }
          }}
        />
        <div className="wb-palette-list nowheel" id="wb-palette-list" role="listbox" ref={listRef}>
          {items.map((it, i) => (
            <div
              key={it.key}
              id={`wb-pal-${i}`}
              role="option"
              aria-selected={i === active}
              className={`wb-palette-item ${i === active ? 'is-active' : ''}`}
              onMouseMove={() => i !== active && setActive(i)}
              onClick={(e) => choose(i, e.ctrlKey || e.metaKey)}
            >
              {it.file ? <FileIcon name={it.file} /> : it.icon && <Icon name={it.icon} className="wb-icon" />}
              <span className="wb-palette-label truncate">{it.label}</span>
              {it.detail && <span className="wb-palette-detail truncate">{it.detail}</span>}
              {it.keys && <kbd className="wb-palette-keys">{it.keys}</kbd>}
            </div>
          ))}
          {!items.length && (
            <p className="wb-palette-msg">{!pick && !value.startsWith('>') && !value.startsWith('#') && files === null ? 'Listing the project’s files…' : value.startsWith('#') && !ws.lsp.servers.some((s) => s.available) ? 'Symbols across the project need a language server.' : 'Nothing matches.'}</p>
          )}
          {filesNote && !value.startsWith('>') && <p className="wb-palette-msg">{filesNote}</p>}
        </div>
      </div>
    </div>,
    document.body,
  );
}
