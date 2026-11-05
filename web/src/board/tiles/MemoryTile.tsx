// Memory tile (node kind "memory", docs/API-orchestration.md §12): a stack of memories, built
// like lego. The project's own memory is the base and always on; blocks from all projects or
// from another project go on top. Connect it to consoles, groups, orchestrator cards or the
// editor and every agent behind them works from the same stack: at launch, through the memory
// tools, and typed once, only what each one does not have yet.

import { memo, useEffect, useMemo, useState, useSyncExternalStore } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import type { MenuEntry } from '../../components/Menu';
import { Modal } from '../../components/Modal';
import { toast } from '../../components/Toast';
import { timeAgo } from '../../lib/format';
import { addBlocks, agentsLabel, blockKey, MAIN_DOC, moveBlock, sentMessage, summarizeSend, tokensLabel } from '../../memory/blocks';
import { MemoryPicker } from '../../memory/MemoryPicker';
import { sendTile, orchError } from '../../orch/actions';
import { orchApi } from '../../orch/api';
import { useOrch } from '../../orch/store';
import type { MemoryBlock, MemoryPlan } from '../../orch/types';
import type { FlowNode } from '../flow';
import { readMemoryNode } from '../kinds';
import { useBoardEnv, useUi } from '../ui';
import { TileFrame, TileHead } from './common';

function MemoryTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, ui, project } = useBoardEnv();
  const d = readMemoryNode(data.node);
  const doc = useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  const connecting = useUi((s) => s.connectFrom === id);
  const memoryRev = useOrch((s) => Object.values(s.memoryRev).reduce((n, v) => n + v, 0));
  const [plan, setPlan] = useState<MemoryPlan | null>(null);
  const [adding, setAdding] = useState(false);
  const [added, setAdded] = useState<MemoryBlock[]>([]);
  const [busy, setBusy] = useState(false);
  const [sent, setSent] = useState(0);
  const [dragged, setDragged] = useState<string | null>(null);
  const [over, setOver] = useState<string | null>(null);
  const wires = doc.edges.filter((e) => e.kind === 'wire' && e.source === id);
  const blocksKey = JSON.stringify(d.blocks);
  const wiresKey = wires.map((e) => e.target).join(',');
  const membersKey = doc.nodes.filter((n) => n.kind === 'group' || n.kind === 'terminal').length;

  useEffect(() => {
    let alive = true;
    const t = window.setTimeout(() => {
      // the hub has to see what was just changed on the board
      void ctl.sync
        .flush()
        .then(() => orchApi.memoryPlan(project.id, { memoryNodeId: id, workspaceId: ctl.workspaceId }))
        .then((p) => alive && setPlan(p))
        .catch(() => undefined);
    }, 300);
    return () => {
      alive = false;
      window.clearTimeout(t);
    };
  }, [ctl, id, project.id, blocksKey, wiresKey, membersKey, memoryRev, sent]);

  const sizes = useMemo(() => new Map((plan?.docs ?? []).map((x) => [blockKey(x), x])), [plan]);
  const size = (b: MemoryBlock) => sizes.get(blockKey(b));
  const base = { projectId: project.id, name: MAIN_DOC };
  const total = [base, ...d.blocks.filter((b) => !b.lookup)].reduce((n, b) => n + (size(b)?.chars ?? 0), 0);
  const sum = summarizeSend(plan?.targets ?? []);
  const agents = plan?.targets.length ?? 0;
  const picked = useMemo(() => new Set(added.map(blockKey)), [added]);
  const hidden = useMemo(() => new Set([blockKey(base), ...d.blocks.map(blockKey)]), [blocksKey, project.id]); // eslint-disable-line react-hooks/exhaustive-deps

  const setBlocks = (blocks: MemoryBlock[]) => ctl.updateData(id, { blocks });

  const addPicked = async () => {
    const next = addBlocks(d.blocks, added, project.id);
    setAdding(false);
    setAdded([]);
    if (next.length === d.blocks.length) return;
    setBlocks(next);
    if (d.manual || !wires.length) return;
    await ctl.sync.flush().catch(() => undefined);
    await sendTile(ctl.workspaceId, id);
    setSent((n) => n + 1);
  };

  const send = async (force: boolean) => {
    setBusy(true);
    try {
      await ctl.sync.flush().catch(() => undefined);
      const r = await orchApi.pushMemory(project.id, { memoryNodeId: id, workspaceId: ctl.workspaceId, force });
      if (r.delivered + r.busy) toast.success(sentMessage(r));
      else toast.info(r.upToDate ? 'They already have all of it: nothing was typed.' : 'Nothing was sent: no agent can take it right now.');
      setSent((n) => n + 1);
    } catch (err) {
      toast.error(`Could not send the memory: ${orchError(err)}`);
    } finally {
      setBusy(false);
    }
  };

  const menu: MenuEntry[] = [
    { label: connecting ? 'Finish connecting' : 'Connect…', icon: 'wire', onSelect: () => ui.set({ connectFrom: connecting ? null : id }) },
    { label: 'Add memory…', icon: 'plus', onSelect: () => setAdding(true) },
    { label: 'Open memory', icon: 'brain', shortcut: 'M', onSelect: () => ui.set({ panel: 'memory' }) },
    { type: 'heading', label: 'Sending' },
    { label: 'Send when connected', checked: !d.manual, onSelect: () => ctl.updateData(id, { manual: !d.manual }) },
    { label: 'Send all of it again', icon: 'send', disabled: !agents, onSelect: () => void send(true) },
  ];

  return (
    <TileFrame id={id} kind="memory" selected={!!selected} label="Memory tile" minW={260} minH={220} className={connecting ? 'is-connecting' : ''}>
      <Handle type="target" position={Position.Left} id="in" className="nb-handle nb-handle-in" isConnectable={false} />
      <Handle type="source" position={Position.Right} id="out" className="nb-handle nb-handle-out nb-handle-wire" title="Drag to a console, group, orchestrator or the editor" />
      <TileHead
        id={id}
        icon="brain"
        title="Memory"
        meta={
          <span className="tile-meta tabular" title="Size of what goes in whole, roughly. Blocks left to look up cost nothing until read.">
            {tokensLabel(total)} tokens
          </span>
        }
        menu={menu}
        menuLabel="Memory tile actions"
      />
      <div className="memory-body nodrag nowheel">
        <div className="memory-stack" role="group" aria-label="Memory stack">
          <div className="memory-brick is-base" title="The project's own memory is always on.">
            <Icon name="lock" />
            <span className="memory-brick-name truncate">{project.name}</span>
            <span className="memory-size tabular">{tokensLabel(size(base)?.chars ?? 0)}</span>
          </div>
          {d.blocks.map((b, index) => {
            const info = size(b);
            const name = info?.title ?? b.name;
            const key = blockKey(b);
            return (
              <div
                key={key}
                className={`memory-brick ${info?.missing ? 'is-missing' : ''} ${b.lookup ? 'is-lookup' : ''} ${dragged === key ? 'is-dragged' : ''} ${over === key && dragged !== key ? 'is-drop' : ''}`}
                draggable
                tabIndex={0}
                title="Drag to reorder: what is higher goes first when the budget runs out. Alt+arrow keys do the same."
                onDragStart={(e) => {
                  e.stopPropagation();
                  e.dataTransfer.effectAllowed = 'move';
                  e.dataTransfer.setData('text/plain', key);
                  setDragged(key);
                }}
                onDragOver={(e) => {
                  if (!dragged) return;
                  e.preventDefault();
                  e.stopPropagation();
                  setOver(key);
                }}
                onDrop={(e) => {
                  e.preventDefault();
                  e.stopPropagation();
                  if (dragged) setBlocks(moveBlock(d.blocks, dragged, key));
                  setDragged(null);
                  setOver(null);
                }}
                onDragEnd={() => {
                  setDragged(null);
                  setOver(null);
                }}
                onKeyDown={(e) => {
                  if (!e.altKey || (e.key !== 'ArrowUp' && e.key !== 'ArrowDown')) return;
                  const neighbour = d.blocks[index + (e.key === 'ArrowUp' ? -1 : 1)];
                  if (!neighbour) return;
                  e.preventDefault();
                  e.stopPropagation();
                  setBlocks(moveBlock(d.blocks, key, blockKey(neighbour)));
                }}
              >
                <span className="memory-brick-name truncate" title={info?.missing ? 'This memory no longer exists.' : `${name} · ${info?.owner ?? ''}`}>
                  {name}
                  {info?.owner && info.owner !== name && <span className="memory-brick-owner"> · {info.owner}</span>}
                </span>
                <span className="memory-size tabular">{info?.missing ? 'gone' : b.lookup ? 'look up' : tokensLabel(info?.chars ?? 0)}</span>
                <Button
                  size="sm"
                  variant="ghost"
                  iconOnly
                  icon="search"
                  aria-pressed={!!b.lookup}
                  aria-label={b.lookup ? `Send ${name} whole` : `Leave ${name} to look up`}
                  title={b.lookup ? 'Left out: agents read it by name when needed. Click to send it whole.' : 'Sent whole. Click to leave it out: agents read it by name when needed.'}
                  onClick={() => setBlocks(d.blocks.map((x) => (blockKey(x) === blockKey(b) ? { ...x, lookup: !x.lookup } : x)))}
                />
                <Button size="sm" variant="ghost" iconOnly icon="close" aria-label={`Remove ${name}`} onClick={() => setBlocks(d.blocks.filter((x) => blockKey(x) !== blockKey(b)))} />
              </div>
            );
          })}
          <button type="button" className="memory-brick is-add" onClick={() => setAdding(true)}>
            <Icon name="plus" />
            <span>Add memory</span>
          </button>
        </div>
        <div className="memory-push">
          <span className="tile-hint truncate" role="status">
            {!wires.length
              ? 'Connect it to agents'
              : !agents
                ? 'No agent behind it yet'
                : sum.send.length
                  ? `${sum.send.length} of ${agentsLabel(agents)} behind · ${tokensLabel(sum.chars)} tokens`
                  : `${agentsLabel(agents)} up to date${d.lastPush?.at ? ` · sent ${timeAgo(d.lastPush.at)}` : ''}`}
          </span>
          <span className="spacer" />
          {!wires.length ? (
            <Button size="sm" icon="wire" onClick={() => ui.set({ connectFrom: id })}>
              Connect
            </Button>
          ) : (
            sum.send.length > 0 && (
              <Button size="sm" variant="primary" icon="send" loading={busy} onClick={() => void send(false)}>
                Send
              </Button>
            )
          )}
        </div>
      </div>
      {adding && (
        <Modal
          title="Add memory to this stack"
          icon="brain"
          onClose={() => {
            setAdding(false);
            setAdded([]);
          }}
          footer={
            <>
              <Button
                variant="secondary"
                onClick={() => {
                  setAdding(false);
                  setAdded([]);
                }}
              >
                Cancel
              </Button>
              <Button variant="primary" icon="plus" disabled={!added.length} onClick={() => void addPicked()}>
                {d.manual || !wires.length ? `Add ${added.length || ''}` : `Add ${added.length || ''} and send`}
              </Button>
            </>
          }
        >
          <MemoryPicker
            projectId={project.id}
            picked={picked}
            hidden={hidden}
            onToggle={(b, on) => setAdded((cur) => (on ? [...cur, b] : cur.filter((x) => blockKey(x) !== blockKey(b))))}
          />
        </Modal>
      )}
    </TileFrame>
  );
}

export const MemoryTile = memo(MemoryTileImpl);
