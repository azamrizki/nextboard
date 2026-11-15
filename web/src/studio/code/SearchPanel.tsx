// Search and replace in files (Ctrl+Shift+F): match case, whole word, regular expression
// (Alt+C, Alt+W, Alt+R), files to include and exclude. The hub searches on each part's machine
// and skips ignored and binary files; a Root card searches every part ("admin/**" only admin) and
// shows the results under each part's name. The toggles and the query are remembered.

import { useEffect, useRef, useState, type KeyboardEvent } from 'react';
import { errorMessage, isCode } from '../../api';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { FileIcon } from './Explorer';
import type { FileKey } from './paths';
import { useWs } from './useWs';
import { nameOf, type SearchState, type ViewSearch, type Workspace } from './workspace';

const DEBOUNCE_MS = 300;

export function SearchPanel({ ws, focusTick }: { ws: Workspace; focusTick: number }) {
  useWs(ws);
  const s = ws.search;
  const [res, setRes] = useState<ViewSearch | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [closed, setClosed] = useState<Set<string>>(new Set());
  const [showReplace, setShowReplace] = useState(!!s.replace);
  const [showFilters, setShowFilters] = useState(!!(s.include || s.exclude));
  const [confirm, setConfirm] = useState<FileKey[] | null>(null);
  const [rev, setRev] = useState(0);
  const queryRef = useRef<HTMLInputElement>(null);

  const set = (patch: Partial<SearchState>) => {
    ws.search = { ...ws.search, ...patch };
    ws.changed();
  };

  useEffect(() => {
    queryRef.current?.focus();
    queryRef.current?.select();
  }, [focusTick]);

  useEffect(() => {
    if (!s.query) {
      setRes(null);
      setError('');
      setBusy(false);
      return;
    }
    const ctl = new AbortController();
    const t = window.setTimeout(() => {
      setBusy(true);
      ws
        .searchParts({ query: s.query, matchCase: s.matchCase, wholeWord: s.wholeWord, regex: s.regex, include: s.include, exclude: s.exclude }, ctl.signal)
        .then((r) => {
          setRes(r);
          setError('');
          setBusy(false);
        })
        .catch((err) => {
          if (ctl.signal.aborted) return;
          setRes(null);
          setBusy(false);
          setError(isCode(err, 'bad_pattern') ? 'That is not a valid regular expression.' : errorMessage(err));
        });
    }, DEBOUNCE_MS);
    return () => {
      window.clearTimeout(t);
      ctl.abort();
    };
  }, [ws, s.query, s.matchCase, s.wholeWord, s.regex, s.include, s.exclude, rev]);

  const onAlt = (e: KeyboardEvent) => {
    if (!e.altKey) return;
    const k = e.key.toLowerCase();
    if (k === 'c') set({ matchCase: !s.matchCase });
    else if (k === 'w') set({ wholeWord: !s.wholeWord });
    else if (k === 'r') set({ regex: !s.regex });
    else return;
    e.preventDefault();
  };

  const toggle = (key: string) => {
    setClosed((cur) => {
      const n = new Set(cur);
      if (n.has(key)) n.delete(key);
      else n.add(key);
      return n;
    });
  };

  /** Files with unsaved edits stay out: the hub writes to disk, the edits live here. Each part gets its own files. */
  const replaceIn = async (keys: FileKey[]) => {
    try {
      const r = await ws.replaceParts({ query: s.query, matchCase: s.matchCase, wholeWord: s.wholeWord, regex: s.regex, replacement: s.replace }, keys);
      if (!r) return;
      if (r.skipped.length) toast.notify('warning', 'Skipped files with unsaved edits', r.skipped.map((k) => ws.relLabel(k)).join(', '));
      if (r.failed.length) toast.error(`${ws.relLabel(r.failed[0].key)}: ${r.failed[0].error}${r.failed.length > 1 ? ` (and ${r.failed.length - 1} more)` : ''}`);
      if (r.skipped.length < keys.length) toast.success(`Replaced ${r.count} ${r.count === 1 ? 'match' : 'matches'}.`);
      setRev((n) => n + 1);
    } catch (err) {
      toast.error(`Replace failed: ${errorMessage(err)}`);
    }
  };

  const sections = res?.parts ?? [];
  const files = sections.flatMap((p) => p.files);
  const count = res?.count ?? 0;
  const multi = ws.paths.multi;

  return (
    <div className="wb-view">
      <header className="wb-view-head">
        <h2>Search</h2>
        <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Search again" title="Search again" onClick={() => setRev((n) => n + 1)} />
        <Button size="sm" variant={showFilters ? 'secondary' : 'ghost'} iconOnly icon="sliders" aria-label="Files to include and exclude" aria-pressed={showFilters} title="Files to include and exclude" onClick={() => setShowFilters((v) => !v)} />
      </header>
      <div className="wb-search-form">
        <div className="wb-search-row">
          <button type="button" className="wb-search-chev" aria-label={showReplace ? 'Hide replace' : 'Show replace'} aria-expanded={showReplace} title="Replace" onClick={() => setShowReplace((v) => !v)}>
            <Icon name={showReplace ? 'chevronDown' : 'chevronRight'} />
          </button>
          <div className="wb-search-input">
            <input ref={queryRef} aria-label="Search" placeholder="Search" spellCheck={false} value={s.query} onChange={(e) => set({ query: e.target.value })} onKeyDown={onAlt} />
            <button type="button" className={`wb-toggle ${s.matchCase ? 'is-on' : ''}`} aria-label="Match case (Alt+C)" aria-pressed={s.matchCase} title="Match case · Alt+C" onClick={() => set({ matchCase: !s.matchCase })}>
              Aa
            </button>
            <button type="button" className={`wb-toggle ${s.wholeWord ? 'is-on' : ''}`} aria-label="Match whole word (Alt+W)" aria-pressed={s.wholeWord} title="Match whole word · Alt+W" onClick={() => set({ wholeWord: !s.wholeWord })}>
              ab
            </button>
            <button type="button" className={`wb-toggle ${s.regex ? 'is-on' : ''}`} aria-label="Regular expression (Alt+R)" aria-pressed={s.regex} title="Regular expression · Alt+R" onClick={() => set({ regex: !s.regex })}>
              .*
            </button>
          </div>
        </div>
        {showReplace && (
          <div className="wb-search-row is-indented">
            <div className="wb-search-input">
              <input aria-label="Replace" placeholder="Replace" spellCheck={false} value={s.replace} onChange={(e) => set({ replace: e.target.value })} onKeyDown={onAlt} />
            </div>
            <Button size="sm" variant="ghost" iconOnly icon="check" aria-label="Replace all" title="Replace all" disabled={!count} onClick={() => setConfirm(files.map((f) => f.key))} />
          </div>
        )}
        {showFilters && (
          <>
            <div className="wb-search-row is-indented">
              <div className="wb-search-input">
                <input aria-label="Files to include" placeholder={multi ? `Files to include (*.ts, ${ws.parts[0].name}/**)` : 'Files to include (*.ts, src/**)'} spellCheck={false} value={s.include} onChange={(e) => set({ include: e.target.value })} />
              </div>
            </div>
            <div className="wb-search-row is-indented">
              <div className="wb-search-input">
                <input aria-label="Files to exclude" placeholder="Files to exclude (dist, *.min.js)" spellCheck={false} value={s.exclude} onChange={(e) => set({ exclude: e.target.value })} />
              </div>
            </div>
          </>
        )}
      </div>
      <div className="wb-search-summary" role="status">
        {busy ? 'Searching…' : error ? <span className="is-error">{error}</span> : res ? (count ? `${count.toLocaleString()}${res.truncated ? '+' : ''} ${count === 1 ? 'result' : 'results'} in ${files.length} ${files.length === 1 ? 'file' : 'files'}` : 'No results.') : ''}
        {res?.truncated && !busy && <span className="text-3"> Narrow the search to see the rest.</span>}
      </div>
      <div className="wb-results nowheel">
        {sections.map((sec) => (
          <div key={sec.part.partId} className="wb-result-part">
            {/* a Root card: the results of each part under its name */}
            {multi && (
              <div className="wb-section-title wb-part-head" title={`${sec.part.name}: ${sec.part.root}`}>
                <Icon name="folder" className="wb-icon" />
                <span className="truncate">{sec.part.name}</span>
                {sec.error !== undefined && <span className="is-error truncate">{errorMessage(sec.error)}</span>}
                <span className="spacer" />
                <span className="wb-result-count tabular">
                  {sec.count.toLocaleString()}
                  {sec.truncated ? '+' : ''}
                </span>
              </div>
            )}
            {!multi && sec.error !== undefined && <div className="wb-row-msg is-error">{errorMessage(sec.error)}</div>}
            {sec.files.map((f) => {
              const isClosed = closed.has(f.key);
              const name = nameOf(f.rel);
              const dir = f.rel.slice(0, Math.max(0, f.rel.length - name.length - 1));
              const abs = f.key;
              return (
                <div key={f.key} className="wb-result-file">
                  <div className="wb-row wb-result-head" role="button" tabIndex={0} aria-expanded={!isClosed} title={ws.relLabel(f.key)} onClick={() => toggle(f.key)} onKeyDown={(e) => e.key === 'Enter' && toggle(f.key)}>
                    <Icon name={isClosed ? 'chevronRight' : 'chevronDown'} className="wb-chev" />
                    <FileIcon name={name} />
                    <span className="wb-row-name truncate">{name}</span>
                    {dir && <span className="wb-result-dir truncate">{dir}</span>}
                    {showReplace && (
                      <button
                        type="button"
                        className="wb-result-act"
                        aria-label={`Replace in ${name}`}
                        title="Replace in this file"
                        onClick={(e) => {
                          e.stopPropagation();
                          void replaceIn([f.key]);
                        }}
                      >
                        <Icon name="check" />
                      </button>
                    )}
                    <span className="wb-result-count tabular">{f.matches.length}</span>
                  </div>
                  {!isClosed &&
                    f.matches.map((m, i) => (
                      <button
                        key={`${m.line}:${m.col}:${i}`}
                        type="button"
                        className="wb-hit"
                        title={`Line ${m.line}`}
                        onClick={() => ws.open(abs, { line: m.line, col: m.col + 1, endCol: m.col + m.len + 1, source: true })}
                        onDoubleClick={() => ws.open(abs, { pin: true, line: m.line, col: m.col + 1, endCol: m.col + m.len + 1, source: true })}
                      >
                        <span className="wb-hit-line tabular">{m.line}</span>
                        <span className="wb-hit-text mono truncate">
                          {m.text.slice(0, m.textCol).trimStart()}
                          <mark className={showReplace && s.replace ? 'is-old' : undefined}>{m.text.slice(m.textCol, m.textCol + m.len)}</mark>
                          {showReplace && s.replace && !s.regex && <mark className="is-new">{s.replace}</mark>}
                          {m.text.slice(m.textCol + m.len)}
                        </span>
                      </button>
                    ))}
                </div>
              );
            })}
          </div>
        ))}
      </div>
      {confirm && (
        <ConfirmDialog
          title={`Replace ${count.toLocaleString()} ${count === 1 ? 'match' : 'matches'}?`}
          confirmLabel="Replace all"
          danger
          onClose={() => setConfirm(null)}
          onConfirm={() => replaceIn(confirm)}
        >
          <p>
            This changes {confirm.length} {confirm.length === 1 ? 'file' : 'files'} on disk, replacing “{s.query}” with “{s.replace}”. Undo in the editor cannot bring it back; use Git for that.
          </p>
        </ConfirmDialog>
      )}
    </div>
  );
}
