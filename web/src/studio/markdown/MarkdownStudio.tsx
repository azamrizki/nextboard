// Markdown studio (E5, D; docs/API-studio.md §8): the project's notes with Claude's agent
// memory first, three views — Reading (relative .md links open here), Source (autosave
// 1.5 s after typing stops) and Graph (wiki + markdown links, orphans too) — backlinks,
// live reload when an agent edits the open note (polled every 3 s), PDF and Add to board.

import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, errorMessage, fsRawUrl } from '../../api';
import type { BoardController } from '../../board/controller';
import { rectOf } from '../../board/controller';
import { DEFAULT_SIZE } from '../../board/kinds';
import { placeNear } from '../../board/layout';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { SegmentedControl } from '../../components/form';
import { Icon } from '../../components/Icon';
import { useStudioPane } from '../StudioPane';
import { ContextMenu, type MenuEntry } from '../../components/Menu';
import { Modal } from '../../components/Modal';
import { toast } from '../../components/Toast';
import { timeAgo } from '../../lib/format';
import type { Project } from '../../lib/types';
import { fsApi, mdApi } from '../api';
import { studioStore } from '../store';
import { CodeEditor } from '../code/CodeEditor';
import { useRemoteConfirm } from '../confirm';
import { printMarkdownUrl } from '../documents/model';
import { MarkdownView } from '../MarkdownView';
import type { MdBacklinks, MdFile, MdGraph, MdVault } from '../types';
import { useFileDoc } from '../useFileDoc';
import '../../styles/studio.css';

const Graph = lazy(() => import('./Graph'));

type View = 'reading' | 'source' | 'graph';
interface Props {
  project: Project;
  ctl: BoardController;
  initialPath?: string;
  viewCenter: () => { x: number; y: number };
  onClose: () => void;
}

const dirOf = (p: string) => p.slice(0, p.lastIndexOf('/') + 1);
function resolvePath(base: string, href: string): string {
  const parts = (href.startsWith('/') ? href : dirOf(base) + href).split('/');
  const out: string[] = [];
  for (const p of parts) {
    if (p === '..') out.pop();
    else if (p !== '.' && p !== '') out.push(p);
  }
  return '/' + out.join('/');
}

export default function MarkdownStudio({ project, ctl, initialPath, viewCenter, onClose }: Props) {
  const mid = project.machineId;
  const root = project.path.replace(/\/+$/, '');
  const [vault, setVault] = useState<MdVault | null>(null);
  const [vaultErr, setVaultErr] = useState('');
  const [path, setPath] = useState<string | null>(initialPath ?? null);
  const [view, setView] = useState<View>('reading');
  const [q, setQ] = useState('');
  const [graph, setGraph] = useState<MdGraph | null>(null);
  const [backlinks, setBacklinks] = useState<MdBacklinks | null>(null);
  const [menu, setMenu] = useState<{ x: number; y: number; file: MdFile | null } | null>(null);
  const [prompt, setPrompt] = useState<{ kind: 'new' | 'rename' | 'duplicate'; dir: string; file?: MdFile; value: string } | null>(null);
  const [trash, setTrash] = useState<MdFile | null>(null);
  const [diskChanged, setDiskChanged] = useState(false);
  const [confirmDialog, run] = useRemoteConfirm();
  const f = useFileDoc(mid, path, project.id, run);
  const { doc } = f;
  const [saveState, setSaveState] = useState<'' | 'saving' | 'saved' | 'failed'>('');

  const loadVault = useCallback(
    (refresh = false) =>
      mdApi
        .vault(project.id, refresh)
        .then((v) => {
          setVault(v);
          setVaultErr('');
          return v;
        })
        .catch((err) => {
          setVaultErr(errorMessage(err));
          return null;
        }),
    [project.id],
  );

  useEffect(() => {
    void loadVault().then((v) => {
      if (!v || initialPath) return;
      const first = v.agentMemory?.files?.find((x) => /^memory\.md$/i.test(x.rel)) ?? v.files?.find((x) => /^readme\.md$/i.test(x.rel)) ?? v.files?.[0] ?? v.agentMemory?.files?.[0];
      if (first) setPath(first.path);
    });
  }, [loadVault, initialPath]);

  const memFiles = useMemo(() => vault?.agentMemory?.files ?? [], [vault]);
  const files = useMemo(() => vault?.files ?? [], [vault]);
  const isMemory = !!path && memFiles.some((x) => x.path === path);
  const current = useMemo(() => [...files, ...memFiles].find((x) => x.path === path) ?? null, [files, memFiles, path]);
  const relOf = (p: string) => (p.startsWith(root + '/') ? p.slice(root.length + 1) : null);
  const currentRel = path ? relOf(path) : null;

  // graph & backlinks
  useEffect(() => {
    if (view !== 'graph') return;
    mdApi
      .graph(project.id)
      .then(setGraph)
      .catch(() => setGraph({ nodes: [], edges: [], unresolved: 0, truncated: false }));
  }, [view, project.id, vault]);
  useEffect(() => {
    setBacklinks(null);
    if (!currentRel || isMemory) return;
    mdApi
      .backlinks(project.id, currentRel)
      .then(setBacklinks)
      .catch(() => setBacklinks(null));
  }, [project.id, currentRel, isMemory, vault]);

  // live: the hub watches the open note (fs/watch → "fsChanged", 3 s polling on the
  // machine); without a watch (older hub) this client stats it every 3 s itself
  const docRef = useRef(doc);
  docRef.current = doc;
  const dirtyRef = useRef(f.dirty);
  dirtyRef.current = f.dirty;
  useEffect(() => {
    setDiskChanged(false);
    if (!path) return;
    let stop = false;
    let watchId = '';
    let off: (() => void) | null = null;
    let poll = 0;
    let renew = 0;
    const changed = (mtime: string | undefined, exists: boolean) => {
      const d = docRef.current;
      if (stop || d.status !== 'ready' || d.saving || !exists || !mtime || mtime === d.mtime) return;
      if (dirtyRef.current) setDiskChanged(true);
      else void f.reload();
    };
    const fallback = () => {
      poll = window.setInterval(() => {
        fsApi
          .statBatch(mid, [path])
          .then(([st]) => st && changed(st.mtime, st.exists))
          .catch(() => undefined);
      }, 3000);
    };
    fsApi
      .watch(mid, [path], 60)
      .then((w) => {
        if (stop) {
          void fsApi.unwatch(w.watchId).catch(() => undefined);
          return;
        }
        watchId = w.watchId;
        off = studioStore.watchFs(watchId, (ev) => {
          const c = ev.changes?.find((x) => x.path === path) ?? ev.changes?.[0];
          if (c) changed(c.mtime, c.exists);
        });
        renew = window.setInterval(() => void fsApi.renewWatch(watchId).catch(() => undefined), 30_000);
      })
      .catch(fallback);
    return () => {
      stop = true;
      off?.();
      window.clearInterval(poll);
      window.clearInterval(renew);
      if (watchId) void fsApi.unwatch(watchId).catch(() => undefined);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mid, path]);

  // Source view: autosave 1.5 s after typing stops
  useEffect(() => {
    if (view !== 'source' || !f.dirty || doc.conflict || diskChanged || doc.readOnly) return;
    const t = window.setTimeout(() => {
      setSaveState('saving');
      void f.save().then((ok) => setSaveState(ok ? 'saved' : 'failed'));
    }, 1500);
    return () => window.clearTimeout(t);
  }, [view, doc.text, f.dirty, doc.conflict, diskChanged, doc.readOnly]); // eslint-disable-line react-hooks/exhaustive-deps

  const pane = useStudioPane('Notes', !!initialPath);
  const open = (p: string) => {
    pane.showMain();
    if (p === path) return;
    if (f.dirty) void f.save();
    setPath(p);
    setSaveState('');
    if (view === 'graph') setView('reading');
  };

  const createIn = (dir: string) => setPrompt({ kind: 'new', dir, value: '' });
  const submit = async () => {
    if (!prompt) return;
    let name = prompt.value.trim();
    if (!name || name.includes('/')) {
      toast.error('Use a plain name without slashes.');
      return;
    }
    if (!/\.(md|markdown|mdx)$/i.test(name)) name += '.md';
    try {
      if (prompt.kind === 'rename' && prompt.file) {
        const st = await run((c) => fsApi.rename(mid, prompt.file!.path, prompt.dir + name, c));
        if (!st) return;
        if (path === prompt.file.path) setPath(st.path);
      } else {
        const text =
          prompt.kind === 'duplicate' && prompt.file ? (await api.fsRead(mid, prompt.file.path, 2_000_000)).content : `# ${name.replace(/\.(md|markdown|mdx)$/i, '')}\n\n`;
        const st = await run((c) => fsApi.write(mid, { path: prompt.dir + name, content: text, unique: true, projectId: project.id, ...c }));
        if (!st) return;
        setPath(st.path);
        setView('source');
      }
      setPrompt(null);
      void loadVault(true);
    } catch (err) {
      toast.error(errorMessage(err));
    }
  };

  const addToBoard = (file: MdFile) => {
    const s = DEFAULT_SIZE.markdown;
    const rect = placeNear(ctl.nodes.filter((n) => n.kind !== 'ink').map(rectOf), viewCenter(), s.w, s.h);
    ctl.addNodes([ctl.make('file', rect, { machineId: mid, path: file.path, view: 'markdown' })]);
    toast.success(`${file.rel} added to the board as a live card.`);
  };

  const fileMenu = (file: MdFile | null): MenuEntry[] => {
    const items: MenuEntry[] = [
      { label: 'New note at the project root…', icon: 'plus', onSelect: () => createIn(root + '/') },
      { label: 'New note in docs/…', icon: 'plus', onSelect: () => createIn(root + '/docs/') },
    ];
    if (vault?.agentMemory?.dir) items.push({ label: 'New note in agent memory…', icon: 'brain', onSelect: () => createIn(vault.agentMemory!.dir.replace(/\/?$/, '/')) });
    if (file) {
      items.unshift(
        { label: 'Open', icon: 'eye', onSelect: () => open(file.path) },
        { label: 'Add to board', icon: 'plus', onSelect: () => addToBoard(file) },
        { label: 'Rename…', icon: 'pencil', onSelect: () => setPrompt({ kind: 'rename', dir: dirOf(file.path), file, value: file.path.split('/').pop() ?? '' }) },
        { label: 'Duplicate…', icon: 'duplicate', onSelect: () => setPrompt({ kind: 'duplicate', dir: dirOf(file.path), file, value: (file.path.split('/').pop() ?? '').replace(/(\.\w+)$/, ' copy$1') }) },
        { label: 'Copy path', icon: 'link', onSelect: () => void navigator.clipboard?.writeText(file.path).then(() => toast.success('Path copied.')) },
        { label: 'Move to trash…', icon: 'trash', danger: true, onSelect: () => setTrash(file) },
        'sep',
      );
    }
    return items;
  };

  const filtered = (list: MdFile[]) => {
    const term = q.trim().toLowerCase();
    return term ? list.filter((x) => x.rel.toLowerCase().includes(term) || x.title.toLowerCase().includes(term)) : list;
  };

  const row = (x: MdFile, memory = false) => (
    <li key={x.path}>
      <button
        type="button"
        className={`studio-item ${path === x.path ? 'is-active' : ''}`}
        aria-current={path === x.path || undefined}
        onClick={() => open(x.path)}
        onContextMenu={(e) => {
          e.preventDefault();
          setMenu({ x: e.clientX, y: e.clientY, file: x });
        }}
        title={x.path}
      >
        <Icon name={memory ? 'brain' : 'markdown'} />
        <span className="studio-item-text">
          <span className="truncate">{x.title || x.rel}</span>
          <span className="fs-xs text-3 truncate">{memory ? x.rel : x.rel}</span>
        </span>
      </button>
    </li>
  );

  const resolveImage = useCallback(
    (src: string) => {
      if (!path || /^[a-z]+:/i.test(src) || src.startsWith('//')) return null;
      return fsRawUrl(mid, resolvePath(path, src));
    },
    [mid, path],
  );
  const onLink = (href: string) => {
    if (!path) return false;
    const clean = href.split('#')[0];
    if (!/\.(md|markdown|mdx)$/i.test(clean)) return false;
    const target = resolvePath(path, clean);
    if ([...files, ...memFiles].some((x) => x.path === target)) {
      open(target);
      return true;
    }
    toast.info(`${clean} isn’t in this project’s notes.`);
    return true;
  };

  const status = doc.saving || saveState === 'saving' ? 'Saving…' : saveState === 'failed' || doc.error ? 'Save failed' : f.dirty ? 'Unsaved' : saveState === 'saved' ? 'Saved' : '';
  return (
    <Modal title="Markdown" icon="markdown" size="lg" className="studio-modal" onClose={() => (f.dirty ? void f.save().then(onClose) : onClose())}>
      <div className={`studio-split ${pane.splitClass}`}>
        <nav className="studio-side" aria-label="Notes">
          <div className="code-side-bar">
            <div className="input-group has-lead grow">
              <span className="ig-lead">
                <Icon name="search" />
              </span>
              <input className="control control-sm" placeholder="Search notes" aria-label="Search notes by name or path" value={q} onChange={(e) => setQ(e.target.value)} />
            </div>
            <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label="New note" onClick={(e) => setMenu({ x: e.clientX, y: e.clientY, file: null })} />
          </div>
          <div className="code-side-list nowheel">
            {vaultErr && <p className="field-error">{vaultErr}</p>}
            {!vault && !vaultErr && <p className="list-msg">Reading notes…</p>}
            {vault?.agentMemory && (
              <>
                <h3 className="section-title studio-group">Agent memory</h3>
                {memFiles.length ? <ul className="plain studio-list">{filtered(memFiles).map((x) => row(x, true))}</ul> : <p className="tree-msg">Claude hasn’t saved memory for this project yet.</p>}
              </>
            )}
            {vault && (
              <>
                <h3 className="section-title studio-group">Project · {files.length}</h3>
                <ul className="plain studio-list">{filtered(files).map((x) => row(x))}</ul>
                {vault.truncated && <p className="tree-msg">Only the first 2000 notes are listed.</p>}
              </>
            )}
          </div>
        </nav>
        <div className="studio-main">
          {pane.back}
          <div className="code-bar">
            <span className="code-path truncate" title={path ?? ''}>
              {current ? current.title || current.rel : view === 'graph' ? 'Link graph' : 'Pick a note'}
            </span>
            {status && <span className={`save-state ${status === 'Save failed' ? 'is-bad' : ''}`}>{status}</span>}
            <span className="spacer" />
            <SegmentedControl<View>
              value={view}
              onChange={setView}
              size="sm"
              label="View"
              options={[
                { value: 'reading', label: 'Reading' },
                { value: 'source', label: 'Source' },
                { value: 'graph', label: 'Graph' },
              ]}
            />
            {current && view !== 'graph' && (
              <>
                <Button size="sm" variant="ghost" iconOnly icon="printer" aria-label="Export PDF" title="Export PDF" onClick={() => window.open(printMarkdownUrl(mid, current.path, 'a4'), '_blank', 'noopener')} />
                <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label="Add to board" title="Add to board (live card)" onClick={() => addToBoard(current)} />
              </>
            )}
          </div>
          {diskChanged && (
            <div className="studio-banner is-warning" role="alert">
              <Icon name="alert" />
              <span>This note changed on disk while you were editing.</span>
              <span className="spacer" />
              <Button size="sm" onClick={() => void f.reload().then(() => setDiskChanged(false))}>
                Reload
              </Button>
              <Button size="sm" variant="primary" onClick={() => void f.save({ force: true }).then((ok) => ok && setDiskChanged(false))}>
                Keep mine
              </Button>
            </div>
          )}
          {doc.conflict && !diskChanged && (
            <div className="studio-banner is-warning" role="alert">
              <Icon name="alert" />
              <span>Changed on disk before your edit was saved.</span>
              <span className="spacer" />
              <Button size="sm" onClick={() => void f.reload()}>
                Reload
              </Button>
              <Button size="sm" variant="primary" onClick={() => void f.save({ force: true })}>
                Keep mine
              </Button>
            </div>
          )}
          {doc.recovered && (
            <div className="studio-banner" role="status">
              <Icon name="history" />
              <span>Recovered unsaved edits from {timeAgo(new Date(doc.recovered.at).toISOString())}.</span>
              <span className="spacer" />
              <Button size="sm" variant="primary" onClick={f.restoreDraft}>
                Restore
              </Button>
              <Button size="sm" variant="ghost" onClick={f.dropDraft}>
                Discard
              </Button>
            </div>
          )}
          {view === 'graph' ? (
            <Suspense fallback={<p className="list-msg">Loading the graph…</p>}>{graph ? <Graph graph={graph} current={currentRel} onOpen={(rel) => open(`${root}/${rel}`)} /> : <p className="list-msg">Reading links…</p>}</Suspense>
          ) : !path ? (
            <div className="empty-state">
              <span className="empty-icon">
                <Icon name="markdown" />
              </span>
              <h3>No note open</h3>
              <p>Pick a note on the left, or make a new one with +.</p>
            </div>
          ) : doc.status === 'loading' ? (
            <p className="list-msg">Opening…</p>
          ) : doc.status === 'error' ? (
            <p className="field-error">{doc.error}</p>
          ) : view === 'source' ? (
            <CodeEditor path={path} text={doc.text} rev={f.rev} readOnly={!!doc.readOnly} crlf={doc.crlf} wrap onChange={f.setText} onSave={() => void f.save()} label={`Editing ${current?.rel ?? path}`} />
          ) : (
            <div className="md-reading nowheel">
              <MarkdownView text={doc.text} className="md-note" resolveImage={resolveImage} onLink={onLink} />
              {backlinks && (
                <aside className="md-backlinks" aria-label="Backlinks">
                  <h3 className="section-title">Linked from · {backlinks.backlinks?.length ?? 0}</h3>
                  {backlinks.backlinks?.length ? (
                    <ul className="plain">
                      {backlinks.backlinks.map((b, i) => (
                        <li key={i}>
                          <button type="button" className="link-btn" onClick={() => open(`${root}/${b.from}`)}>
                            {b.title || b.from}
                          </button>
                          <span className="fs-xs text-3"> · {b.kind === 'wiki' ? 'wiki link' : 'link'}</span>
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <p className="fs-xs text-3">No other note links here.</p>
                  )}
                </aside>
              )}
            </div>
          )}
        </div>
      </div>
      {menu && <ContextMenu x={menu.x} y={menu.y} label="Note actions" items={fileMenu(menu.file)} onClose={() => setMenu(null)} />}
      {prompt && (
        <Modal
          title={prompt.kind === 'new' ? 'New note' : prompt.kind === 'rename' ? 'Rename note' : 'Duplicate note'}
          size="sm"
          onClose={() => setPrompt(null)}
          footer={
            <>
              <Button onClick={() => setPrompt(null)}>Cancel</Button>
              <Button variant="primary" disabled={!prompt.value.trim()} onClick={() => void submit()}>
                {prompt.kind === 'rename' ? 'Rename' : 'Create'}
              </Button>
            </>
          }
        >
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void submit();
            }}
          >
            <label className="field-label" htmlFor="md-name">
              Name in {prompt.dir.startsWith(root) ? prompt.dir.slice(root.length + 1) || project.name : 'agent memory'}
            </label>
            <input id="md-name" className="control mono" autoFocus value={prompt.value} placeholder="notes.md" onChange={(e) => setPrompt({ ...prompt, value: e.target.value })} />
          </form>
        </Modal>
      )}
      {trash && (
        <ConfirmDialog
          title={`Move ${trash.rel} to the trash?`}
          confirmLabel="Move to trash"
          danger
          onClose={() => setTrash(null)}
          onConfirm={async () => {
            const r = await run((c) => fsApi.trash(mid, trash.path, project.id, c));
            if (!r) return;
            if (path === trash.path) setPath(null);
            void loadVault(true);
          }}
        >
          <p>It moves to the project’s trash folder, so it can be brought back by hand.</p>
        </ConfirmDialog>
      )}
      {confirmDialog}
    </Modal>
  );
}
