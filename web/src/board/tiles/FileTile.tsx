// File cards (docs/API-board.md "file"):
//   code     — read-only snapshot with syntax highlighting (does not follow edits)
//   markdown — live: re-rendered when fs/stat reports a new mtime (polled every 3 s
//              while the card is on screen)
//   file     — generic card: size, date, download, "missing" when the file is gone
//   folder   — up to 40 files; a click opens one as a card next to it

import { memo, useEffect, useMemo, useRef, useState, type RefObject } from 'react';
import type { NodeProps } from '@xyflow/react';
import { api, errorMessage, fsRawUrl } from '../../api';
import { Button } from '../../components/Button';
import { Icon, type IconName } from '../../components/Icon';
import type { MenuEntry } from '../../components/Menu';
import { toast } from '../../components/Toast';
import { formatDateTime, timeAgo } from '../../lib/format';
import type { DirEntry } from '../../lib/types';
import { addPath, snapshotOf } from '../files';
import type { FlowNode } from '../flow';
import { baseName, dirName, isImagePath, isMarkdownPath, isProbablyText, joinPath, PREVIEW_LINES, readFile, type FileData } from '../kinds';
import { useHighlighter, useMarkdown } from '../lazy';
import { useBoardEnv } from '../ui';
import { TileFrame, TileHead } from './common';

const POLL_MS = 3000;
const FOLDER_MAX = 40;

export function formatBytes(n: number): string {
  if (!(n >= 0)) return '—';
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10240 ? 1 : 0)} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

const VIEW_ICON: Record<FileData['view'], IconName> = { code: 'fileCode', markdown: 'markdown', file: 'file', folder: 'folderOpen' };

/** Is the element on screen (and the tab visible)? Used to pause polling. */
function useOnScreen(ref: RefObject<HTMLElement | null>): boolean {
  const [on, setOn] = useState(true);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof IntersectionObserver === 'undefined') return;
    const io = new IntersectionObserver((entries) => setOn(entries.some((e) => e.isIntersecting)));
    io.observe(el);
    return () => io.disconnect();
  }, [ref]);
  return on;
}

function FileTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const d = readFile(data.node);
  const name = baseName(d.path) || 'File';
  const { ctl } = useBoardEnv();
  const ref = useRef<HTMLDivElement>(null);

  const copyPath = () => {
    void navigator.clipboard?.writeText(d.path).then(
      () => toast.success('Path copied.'),
      () => toast.error('Copying is blocked in this browser.'),
    );
  };

  const refreshSnapshot = async () => {
    if (!d.machineId) return;
    try {
      const r = await api.fsRead(d.machineId, d.path, 800_000);
      if (r.binary) {
        toast.error(`${name} is a binary file.`);
        return;
      }
      ctl.updateData(id, { snapshot: snapshotOf(r.content, r.size, r.mtime, r.truncated) });
      toast.success(`Snapshot of ${name} updated.`);
    } catch (err) {
      toast.error(`Could not read ${name}: ${errorMessage(err)}`);
    }
  };

  const menu: MenuEntry[] = [
    { label: 'Copy path', icon: 'copy', onSelect: copyPath },
    ...(d.view === 'code' && d.machineId
      ? [{ label: 'Refresh snapshot', icon: 'refresh' as const, onSelect: () => void refreshSnapshot() }]
      : []),
    ...(d.view !== 'folder' && d.machineId
      ? [
          {
            label: 'Download',
            icon: 'download' as const,
            onSelect: () => {
              const a = document.createElement('a');
              a.href = fsRawUrl(d.machineId, d.path);
              a.download = name;
              a.click();
            },
          },
        ]
      : []),
    ...(d.view === 'file' && d.machineId && isProbablyText(d.path)
      ? [{ label: 'Open as code', icon: 'fileCode' as const, onSelect: () => void openAsCode() }]
      : []),
  ];

  const openAsCode = async () => {
    try {
      const r = await api.fsRead(d.machineId, d.path, 800_000);
      if (r.binary) {
        toast.error(`${name} is a binary file.`);
        return;
      }
      ctl.updateData(id, { view: 'code', snapshot: snapshotOf(r.content, r.size, r.mtime, r.truncated) });
    } catch (err) {
      toast.error(`Could not read ${name}: ${errorMessage(err)}`);
    }
  };

  const badge = d.view === 'code' ? 'Snapshot' : d.view === 'markdown' ? 'Live' : d.view === 'folder' ? 'Folder' : null;

  return (
    <TileFrame id={id} kind="file" selected={!!selected} label={`${name} (${d.view})`} minW={200} minH={96} className={`file-${d.view}`}>
      <TileHead
        id={id}
        hint={d.path}
        icon={VIEW_ICON[d.view]}
        title={name}
        meta={badge && <span className={`badge ${d.view === 'markdown' ? 'badge-success' : ''}`}>{badge}</span>}
        actions={
          d.view === 'code' && d.snapshot ? (
            <Button
              size="sm"
              variant="ghost"
              iconOnly
              icon="copy"
              className="nodrag"
              aria-label={`Copy contents of ${name}`}
              title="Copy contents"
              onClick={() =>
                void navigator.clipboard?.writeText(d.snapshot!.content).then(
                  () => toast.success('Copied.'),
                  () => toast.error('Copying is blocked in this browser.'),
                )
              }
            />
          ) : undefined
        }
        menu={menu}
        menuLabel={`Actions for ${name}`}
      />
      <div className="tile-body nodrag nowheel" ref={ref}>
        {d.view === 'code' && <CodeView d={d} />}
        {d.view === 'markdown' && <MarkdownView d={d} visibleRef={ref} />}
        {d.view === 'file' && <GenericView d={d} />}
        {d.view === 'folder' && <FolderView id={id} d={d} />}
      </div>
    </TileFrame>
  );
}

export const FileTile = memo(FileTileImpl);

// ---- code snapshot ----

function CodeView({ d }: { d: FileData }) {
  const hl = useHighlighter();
  const snap = d.snapshot;
  const { text, cut } = useMemo(() => {
    const content = snap?.content ?? '';
    let n = 0;
    let i = -1;
    while (n < PREVIEW_LINES && (i = content.indexOf('\n', i + 1)) >= 0) n++;
    return i >= 0 && n >= PREVIEW_LINES ? { text: content.slice(0, i), cut: true } : { text: content, cut: false };
  }, [snap?.content]);
  const html = useMemo(() => (hl ? hl.highlight(text, d.path) : null), [hl, text, d.path]);
  if (!snap) return <div className="tile-placeholder">No snapshot stored.</div>;
  return (
    <>
      <pre className="code-pre">
        {html ? <code className="hljs" dangerouslySetInnerHTML={{ __html: html.html }} /> : <code>{text}</code>}
      </pre>
      <div className="tile-foot-meta">
        <span>{formatBytes(snap.size)}</span>
        <span>Snapshot · {snap.mtime ? formatDateTime(snap.mtime) : 'unknown date'}</span>
        {(snap.truncated || cut) && <span className="text-warning">Preview truncated</span>}
      </div>
    </>
  );
}

// ---- live markdown ----

function MarkdownView({ d, visibleRef }: { d: FileData; visibleRef: RefObject<HTMLElement | null> }) {
  const mdLib = useMarkdown();
  const onScreen = useOnScreen(visibleRef);
  const [content, setContent] = useState<string | null>(null);
  const [mtime, setMtime] = useState('');
  const [state, setState] = useState<'loading' | 'ok' | 'missing' | 'error'>('loading');
  const [error, setError] = useState('');
  const mtimeRef = useRef('');

  useEffect(() => {
    if (!d.machineId || !d.path) return;
    let alive = true;
    const ctrl = new AbortController();
    const read = async () => {
      const r = await api.fsRead(d.machineId, d.path, 800_000, ctrl.signal);
      if (!alive) return;
      mtimeRef.current = r.mtime;
      setMtime(r.mtime);
      setContent(r.binary ? '' : r.content);
      setState('ok');
    };
    const tick = async () => {
      if (document.hidden) return;
      try {
        const st = await api.fsStat(d.machineId, d.path, ctrl.signal);
        if (!alive) return;
        if (!st.exists) {
          setState('missing');
          mtimeRef.current = '';
          return;
        }
        if (st.mtime !== mtimeRef.current) await read();
      } catch (err) {
        if (!alive || ctrl.signal.aborted) return;
        setError(errorMessage(err));
        setState((s) => (s === 'ok' ? s : 'error'));
      }
    };
    if (mtimeRef.current === '') void tick();
    const timer = onScreen ? window.setInterval(() => void tick(), POLL_MS) : undefined;
    return () => {
      alive = false;
      ctrl.abort();
      window.clearInterval(timer);
    };
  }, [d.machineId, d.path, onScreen]);

  const html = useMemo(() => {
    if (!mdLib || content === null) return '';
    const dir = dirName(d.path);
    return mdLib.renderMarkdown(content, (src) => {
      if (!src || /^[a-z][a-z0-9+.-]*:/i.test(src) || src.startsWith('//')) return null;
      const path = src.startsWith('/') ? src : joinPath(dir, src.replace(/^\.\//, ''));
      return isImagePath(path) ? fsRawUrl(d.machineId, path) : null;
    });
  }, [mdLib, content, d.path, d.machineId]);

  if (state === 'missing')
    return (
      <div className="tile-placeholder">
        <Icon name="alertCircle" />
        <span>File deleted or moved</span>
        <span className="mono truncate">{d.path}</span>
      </div>
    );
  if (state === 'error' && content === null)
    return (
      <div className="tile-placeholder">
        <Icon name="alertCircle" />
        <span>Could not read the file</span>
        <span className="fs-xs">{error}</span>
      </div>
    );
  if (content === null || !mdLib)
    return (
      <div className="tile-placeholder" role="status">
        <span className="spinner" aria-hidden="true" />
        Loading…
      </div>
    );
  return (
    <>
      <div className="md-body" dangerouslySetInnerHTML={{ __html: html }} />
      <div className="tile-foot-meta">
        <span className="dot dot-success" aria-hidden="true" />
        <span>Live · updated {timeAgo(mtime)}</span>
      </div>
    </>
  );
}

// ---- generic file ----

function GenericView({ d }: { d: FileData }) {
  const [stat, setStat] = useState<{ exists: boolean; size: number; mtime: string } | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    if (!d.machineId) return;
    const ctrl = new AbortController();
    api
      .fsStat(d.machineId, d.path, ctrl.signal)
      .then((s) => setStat(s))
      .catch((err) => !ctrl.signal.aborted && setError(errorMessage(err)));
    return () => ctrl.abort();
  }, [d.machineId, d.path]);
  return (
    <div className="file-generic">
      <span className="mono truncate file-path" title={d.path}>
        {d.path}
      </span>
      {stat && !stat.exists ? (
        <span className="text-danger fs-xs">Missing — the file was moved or deleted.</span>
      ) : stat ? (
        <span className="text-3 fs-xs tabular">
          {formatBytes(stat.size)} · {formatDateTime(stat.mtime)}
        </span>
      ) : error ? (
        <span className="text-danger fs-xs">{error}</span>
      ) : (
        <span className="text-3 fs-xs">Checking…</span>
      )}
    </div>
  );
}

// ---- folder ----

function FolderView({ id, d }: { id: string; d: FileData }) {
  const { ctl } = useBoardEnv();
  const [entries, setEntries] = useState<DirEntry[] | null>(null);
  const [error, setError] = useState('');
  const [reload, setReload] = useState(0);
  useEffect(() => {
    const ctrl = new AbortController();
    setError('');
    api
      .listDir(d.machineId, d.path, ctrl.signal)
      .then((l) => setEntries((l.entries ?? []).filter((e) => !e.dir && !e.name.startsWith('.')).sort((a, b) => a.name.localeCompare(b.name))))
      .catch((err) => !ctrl.signal.aborted && setError(errorMessage(err)));
    return () => ctrl.abort();
  }, [d.machineId, d.path, reload]);

  const open = (e: DirEntry) => {
    const self = ctl.node(id);
    if (!self) return;
    const path = joinPath(d.path, e.name);
    void addPath(ctl, d.machineId, path, { x: self.x + self.w + 40, y: self.y }, { centre: false });
  };

  if (error)
    return (
      <div className="tile-placeholder">
        <Icon name="alertCircle" />
        <span>{error}</span>
        <Button size="sm" icon="refresh" onClick={() => setReload((n) => n + 1)}>
          Try again
        </Button>
      </div>
    );
  if (!entries)
    return (
      <div className="tile-placeholder" role="status">
        <span className="spinner" aria-hidden="true" />
        Loading…
      </div>
    );
  const shown = entries.slice(0, FOLDER_MAX);
  return (
    <>
      <ul className="folder-list" aria-label={`Files in ${d.path}`}>
        {shown.length === 0 && <li className="tile-empty">No files in this folder.</li>}
        {shown.map((e) => (
          <li key={e.name}>
            <button type="button" className="folder-entry" onClick={() => open(e)} title={`Open ${e.name} as a card`}>
              <Icon name={isImagePath(e.name) ? 'image' : isMarkdownPath(e.name) ? 'markdown' : 'file'} />
              <span className="truncate">{e.name}</span>
              {typeof e.size === 'number' && <span className="folder-size tabular">{formatBytes(e.size)}</span>}
            </button>
          </li>
        ))}
      </ul>
      <div className="tile-foot-meta">
        <span>
          {entries.length > FOLDER_MAX ? `${FOLDER_MAX} of ${entries.length} files shown` : `${entries.length} file${entries.length === 1 ? '' : 's'}`}
        </span>
        <button type="button" className="link-btn" onClick={() => setReload((n) => n + 1)}>
          Refresh
        </button>
      </div>
    </>
  );
}
