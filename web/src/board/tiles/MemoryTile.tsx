// Memory card (node kind "memory", docs/API-orchestration.md §12, D21): shows the facts
// of the project (or global) memory; wire it to consoles or groups and Push to brief them
// as background context. The card keeps the result of the last push.

import { orchError } from '../../orch/actions';
import { memo, useState, useSyncExternalStore } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Button } from '../../components/Button';
import type { MenuEntry } from '../../components/Menu';
import { toast } from '../../components/Toast';
import { timeAgo } from '../../lib/format';
import { orchApi } from '../../orch/api';
import { factsOf, useMemoryDoc } from '../../orch/memoryDoc';
import type { FlowNode } from '../flow';
import { directedTerminals } from '../groups';
import { readMemoryNode } from '../kinds';
import { useBoardEnv, useUi } from '../ui';
import { TileFrame, TileHead } from './common';

function MemoryTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, ui, project } = useBoardEnv();
  const d = readMemoryNode(data.node);
  const doc = useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  const mem = useMemoryDoc(project.id);
  const connecting = useUi((s) => s.connectFrom === id);
  const [busy, setBusy] = useState(false);
  const content = mem.data?.[d.scope]?.content ?? '';
  const facts = factsOf(content);
  const wired = doc.edges.filter((e) => e.kind === 'wire' && e.source === id).length;
  const reach = directedTerminals(doc.nodes, doc.edges, id).length;

  const push = async () => {
    setBusy(true);
    try {
      const r = await orchApi.pushMemory(project.id, { memoryNodeId: id, scopes: [d.scope] });
      const lastPush = { at: new Date().toISOString(), delivered: r.delivered, busy: r.busy, skipped: r.skipped + (r.notAgent ?? 0) };
      ctl.apply([{ op: 'patch', id, set: { data: { lastPush } } }], { history: false });
      if (r.delivered) toast.success(`Memory sent to ${r.delivered === 1 ? '1 agent' : `${r.delivered} agents`}${r.busy ? `; ${r.busy} busy will get it when free` : ''}.`);
      else toast.info(r.busy ? `All ${r.busy} wired agents are busy; they get the memory when they are free.` : 'Nothing was sent: no running agent is wired to this card.');
    } catch (err) {
      toast.error(`Could not push the memory: ${orchError(err)}`);
    } finally {
      setBusy(false);
    }
  };

  const menu: MenuEntry[] = [
    { label: connecting ? 'Finish connecting' : 'Connect consoles…', icon: 'wire', onSelect: () => ui.set({ connectFrom: connecting ? null : id }) },
    { label: 'Open memory', icon: 'brain', shortcut: 'M', onSelect: () => ui.set({ panel: 'memory' }) },
    { type: 'heading', label: 'Scope' },
    { label: 'This project', checked: d.scope === 'project', onSelect: () => ctl.updateData(id, { scope: 'project' }) },
    { label: 'All projects', checked: d.scope === 'global', onSelect: () => ctl.updateData(id, { scope: 'global' }) },
  ];

  return (
    <TileFrame id={id} kind="memory" selected={!!selected} label="Memory card" minW={240} minH={160} className={connecting ? 'is-connecting' : ''}>
      <Handle type="target" position={Position.Left} id="in" className="nb-handle nb-handle-in" isConnectable={false} />
      <Handle type="source" position={Position.Right} id="out" className="nb-handle nb-handle-out nb-handle-wire" title="Drag to a console or group to wire it" />
      <TileHead
        id={id}
        icon="brain"
        title={d.scope === 'global' ? 'Memory · all projects' : 'Project memory'}
        meta={<span className="tile-meta tabular">{facts.length === 1 ? '1 fact' : `${facts.length} facts`}</span>}
        menu={menu}
        menuLabel="Memory card actions"
      />
      <div className="memory-body nodrag nowheel">
        {mem.status === 'unsupported' ? (
          <p className="tile-hint">This hub has no project memory yet.</p>
        ) : facts.length === 0 ? (
          <p className="tile-hint">No facts yet. Open the memory to write some or scan the project.</p>
        ) : (
          <ul className="memory-facts">
            {facts.slice(0, 4).map((f, i) => (
              <li key={i} className="truncate" title={f.text}>
                {f.text}
              </li>
            ))}
            {facts.length > 4 && <li className="tile-hint">+ {facts.length - 4} more</li>}
          </ul>
        )}
        <div className="memory-push">
          <span className="tile-hint">
            {d.lastPush
              ? `Pushed ${timeAgo(d.lastPush.at)} · ${d.lastPush.delivered} sent${d.lastPush.busy ? ` · ${d.lastPush.busy} busy` : ''}${d.lastPush.skipped ? ` · ${d.lastPush.skipped} skipped` : ''}`
              : wired
                ? `Wired to ${reach === 1 ? '1 console' : `${reach} consoles`}`
                : 'Wire consoles to brief them'}
          </span>
          <span className="spacer" />
          {wired === 0 ? (
            <Button size="sm" icon="wire" onClick={() => ui.set({ connectFrom: id })}>
              Connect
            </Button>
          ) : (
            <Button size="sm" variant="primary" icon="send" loading={busy} disabled={!facts.length} onClick={() => void push()}>
              Push
            </Button>
          )}
        </div>
      </div>
    </TileFrame>
  );
}

export const MemoryTile = memo(MemoryTileImpl);
