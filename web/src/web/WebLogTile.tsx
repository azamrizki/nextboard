// Console log card (node kind "weblog", docs/API-studio.md §4.5, B19): what the injected
// bridge reported for a preview — console lines, page errors and network calls. The hub
// keeps the last 500 entries per preview (GET /logs, "previewLog" events), so every
// client sees the same log.

import { memo, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import type { NodeProps } from '@xyflow/react';
import { Button } from '../components/Button';
import { SegmentedControl } from '../components/form';
import type { FlowNode } from '../board/flow';
import { TileFrame, TileHead } from '../board/tiles/common';
import { sendTile } from '../orch/actions';
import { useBoardEnv } from '../board/ui';
import { previewApi } from '../studio/api';
import { studioStore, usePreview, useStudio } from '../studio/store';
import type { PreviewLogEntry } from '../studio/types';

type Filter = 'all' | 'errors' | 'network';
const EMPTY: PreviewLogEntry[] = [];

function isError(e: PreviewLogEntry) {
  return e.type === 'error' || (e.type === 'console' && e.level === 'error') || (e.type === 'net' && (e.status === 0 || (e.status ?? 0) >= 400));
}

function WebLogTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl } = useBoardEnv();
  const own = typeof data.node.data.previewId === 'string' ? data.node.data.previewId : '';
  // follow the web tile it is wired to: its preview is re-created after a hub restart
  const doc = useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  const srcEdge = doc.edges.find((e) => e.kind === 'log' && e.target === id);
  const srcNode = srcEdge ? doc.nodes.find((n) => n.id === srcEdge.source) : undefined;
  const linked = srcNode && typeof srcNode.data.previewId === 'string' ? srcNode.data.previewId : '';
  const previewId = linked || own;
  const wired = doc.edges.some((e) => e.kind === 'wire' && e.source === id);
  useEffect(() => {
    if (linked && linked !== own) ctl.apply([{ op: 'patch', id, set: { data: { previewId: linked } } }], { history: false });
  }, [linked, own, ctl, id]);
  const filter: Filter = data.node.data.filter === 'errors' || data.node.data.filter === 'network' ? data.node.data.filter : 'all';
  const preview = usePreview(previewId || undefined);
  const all = useStudio((s) => s.logs[previewId] ?? EMPTY);
  const [clearedAt, setClearedAt] = useState(0);
  const [error, setError] = useState('');
  const listRef = useRef<HTMLDivElement>(null);
  const stick = useRef(true);

  const loaded = useStudio((s) => s.previewsLoaded);
  const known = useStudio((s) => !!s.previews[previewId]);
  useEffect(() => {
    if (!previewId || !loaded) return;
    if (!known) {
      // previews live in the hub's memory: after a restart the web tile makes a new one
      setError('This preview is gone (the hub restarted or it was closed). It comes back when its web tile reopens the page.');
      return;
    }
    setError('');
    previewApi
      .logs(previewId, 0)
      .then((r) => studioStore.addLogs(previewId, r.items ?? []))
      .catch(() => setError('The preview is gone (the hub restarted or it was closed). Reload its web tile to get a new log.'));
  }, [previewId, loaded, known]);

  const shown = useMemo(
    () => all.filter((e) => e.seq > clearedAt && (filter === 'all' || (filter === 'errors' ? isError(e) : e.type === 'net'))),
    [all, clearedAt, filter],
  );
  const errorCount = useMemo(() => all.filter((e) => e.seq > clearedAt && isError(e)).length, [all, clearedAt]);

  useEffect(() => {
    const el = listRef.current;
    if (el && stick.current) el.scrollTop = el.scrollHeight;
  }, [shown.length]);

  return (
    <TileFrame id={id} kind="weblog" selected={!!selected} label="Console log" minW={300} minH={180}>
      <TileHead
        id={id}
        icon="braces"
        title={`Console${preview ? ` · :${preview.targetPort}` : ''}`}
        meta={errorCount > 0 && <span className="badge badge-danger tabular">{errorCount} error{errorCount === 1 ? '' : 's'}</span>}
        actions={
          <>
            {wired && (
              <Button size="sm" variant="ghost" icon="send" className="nodrag" title="Send new errors to the wired consoles" onClick={() => void sendTile(ctl.workspaceId, id)}>
                Send errors
              </Button>
            )}
            <Button size="sm" variant="ghost" className="nodrag" disabled={!all.length} onClick={() => setClearedAt(all[all.length - 1]?.seq ?? 0)}>
              Clear
            </Button>
          </>
        }
        menuLabel="Console log card actions"
      />
      <div className="weblog-filter nodrag">
        <SegmentedControl<Filter>
          value={filter}
          onChange={(v) => ctl.apply([{ op: 'patch', id, set: { data: { filter: v } } }], { history: false })}
          size="sm"
          label="Show"
          options={[
            { value: 'all', label: 'All' },
            { value: 'errors', label: 'Errors' },
            { value: 'network', label: 'Network' },
          ]}
        />
      </div>
      <div
        className="weblog-list nodrag nowheel"
        ref={listRef}
        role="log"
        aria-live="polite"
        onScroll={(e) => {
          const el = e.currentTarget;
          stick.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
      >
        {error && !all.length ? (
          <p className="tile-hint">{error}</p>
        ) : !shown.length ? (
          <p className="tile-hint">{filter === 'all' ? 'Nothing logged yet. Console output, page errors and fetch/XHR calls of the page show up here.' : 'Nothing matches this filter.'}</p>
        ) : (
          shown.map((e) => <LogRow key={e.seq} e={e} />)
        )}
      </div>
    </TileFrame>
  );
}

function LogRow({ e }: { e: PreviewLogEntry }) {
  const time = new Date(e.at).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit' });
  if (e.type === 'net') {
    const bad = e.status === 0 || (e.status ?? 0) >= 400;
    return (
      <div className={`log-row lv-net ${bad ? 'is-bad' : ''}`}>
        <span className="log-time tabular">{time}</span>
        <span className="log-tag">{e.method}</span>
        <span className="log-text truncate" title={e.url}>
          {e.url}
        </span>
        <span className="log-meta tabular">
          {e.status || 'failed'} · {e.ms ?? 0} ms
        </span>
      </div>
    );
  }
  if (e.type === 'error') {
    return (
      <div className="log-row lv-error">
        <span className="log-time tabular">{time}</span>
        <span className="log-tag">error</span>
        <span className="log-text" title={e.stack || e.message}>
          {e.message}
          {e.source ? <span className="log-meta"> · {e.source.split('/').pop()}:{e.line}</span> : null}
        </span>
      </div>
    );
  }
  return (
    <div className={`log-row lv-${e.level ?? 'log'}`}>
      <span className="log-time tabular">{time}</span>
      <span className="log-tag">{e.level ?? 'log'}</span>
      <span className="log-text">{(e.args ?? []).join(' ')}</span>
    </div>
  );
}

export const WebLogTile = memo(WebLogTileImpl);
