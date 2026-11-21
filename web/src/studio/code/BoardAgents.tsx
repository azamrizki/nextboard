// The Code view's board, as its side bar shows it (DESIGN.md §3.19, Code view = Board view): the
// board's consoles by group, each group with the board frame's own menu and the orchestrator
// cards it is linked to; a console's menu links it, puts it in a group or takes it out; New group
// makes one of consoles picked from the list. Then the board's cards (Kanban, the orchestrator,
// a checklist…), each opening as a tab, and New card. Every change is the board's: the Board view
// shows it at once, and the other way round.

import { useContext, useState, useSyncExternalStore } from 'react';
import type { BoardController } from '../../board/controller';
import { sessionOf } from '../../board/controller';
import { createTile } from '../../board/create';
import { groupMenuItems } from '../../board/groupMenu';
import { addToGroupOps, connectIntent, edgeBetween, groupNodes, groupSessions, newGroupOps, nextGroupName } from '../../board/groups';
import { readGroup } from '../../board/kinds';
import { useConsoleActions, useConsoleMenu } from '../../board/NodeHeader';
import { BoardEnvContext } from '../../board/ui';
import { api, errorMessage, isCode } from '../../api';
import { handleProblem } from '../../agents/state';
import { store, useMachine } from '../../store';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Checkbox, TextField } from '../../components/form';
import { Icon } from '../../components/Icon';
import { Menu, type MenuEntry } from '../../components/Menu';
import { Modal } from '../../components/Modal';
import { toast } from '../../components/Toast';
import type { BoardNode, Project, Session } from '../../lib/types';
import { killSessions } from '../../orch/actions';
import { useSessionList } from '../../orch/hooks';
import { ROLE_LABEL, ROLES, type Role } from '../../orch/types';
import { AgentRow, boardFreeSpot, PaneEnv } from './AgentPanes';
import { CARD_KINDS, cardTitle, NEW_CARD_KINDS, opensAsTab, rememberCard } from './cardTabs';
import type { Workspace } from './workspace';

const useDoc = (ctl: BoardController) => useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);

const nameOf = (s: Session) => (s.handle ? `@${s.handle}` : s.name);

/** Link or unlink an orchestrator card and a console or group, as dropping a wire on the board does. */
function toggleLink(ctl: BoardController, project: Project, from: BoardNode, to: BoardNode) {
  const doc = ctl.sync.getState();
  const intent = connectIntent(doc.nodes, doc.edges, from.id, to.id, { workspaceId: ctl.workspaceId, project });
  if (!intent.ok) {
    if (intent.reason) toast.error(intent.reason);
    return;
  }
  ctl.apply([intent.op]);
  toast.success(intent.label);
  if (intent.note) toast.info(intent.note);
}

/** The orchestrator cards a console or group can be linked to, each checked while it is. */
function linkItems(ctl: BoardController, project: Project, target: BoardNode, orchestrators: BoardNode[]): MenuEntry[] {
  const { edges } = ctl.sync.getState();
  if (!orchestrators.length) return [{ label: 'No orchestrator card on this board yet', disabled: true, onSelect: () => undefined }];
  return orchestrators.map((o) => ({ label: cardTitle(o), icon: 'orchestrator' as const, checked: !!edgeBetween(edges, o.id, target.id, 'wire'), onSelect: () => toggleLink(ctl, project, o, target) }));
}

/** Which orchestrator cards a node is linked to. */
function linkedFrom(ctl: BoardController, target: string): BoardNode[] {
  const { nodes, edges } = ctl.sync.getState();
  return edges.filter((e) => e.kind === 'wire' && e.target === target).flatMap((e) => nodes.filter((n) => n.id === e.source && n.kind === 'orchestrator'));
}

export function BoardAgents({ ws, ctl, project, placeOf, onOpen }: { ws: Workspace; ctl: BoardController; project: Project; placeOf: (s: Session) => string; onOpen?: (id: string) => void }) {
  const doc = useDoc(ctl);
  const ui = useContext(BoardEnvContext)?.ui;
  // re-render when a console of this board changes
  useSessionList((s) => s.workspaceId === ctl.workspaceId && s.agentKind === 'agent');
  const [renaming, setRenaming] = useState<BoardNode | null>(null);
  const [confirm, setConfirm] = useState<{ g: BoardNode; what: 'dissolve' | 'kill' } | null>(null);
  const [newGroup, setNewGroup] = useState<string[] | null>(null);
  const groups = groupNodes(doc.nodes);
  const orchestrators = doc.nodes.filter((n) => n.kind === 'orchestrator');
  const consoles = doc.nodes.filter((n) => n.kind === 'terminal' && sessionOf(n)?.agentKind === 'agent');
  const grouped = new Set(groups.flatMap((g) => readGroup(g).members));
  const loose = consoles.filter((n) => !grouped.has(n.id));

  const row = (n: BoardNode) => {
    const s = sessionOf(n);
    return s ? <AgentRow key={n.id} ws={ws} s={s} place={placeOf(s)} onOpen={onOpen} menu={<BoardConsoleMenu ctl={ctl} project={project} nodeId={n.id} session={s} onNewGroup={setNewGroup} />} /> : null;
  };

  return (
    <section className="wb-agents-section wb-board-agents" aria-label="This board's agents">
      <div className="wb-board-actions">
        <Button size="sm" variant="ghost" icon="users" disabled={consoles.length === 0} onClick={() => setNewGroup([])}>
          New group
        </Button>
        {ui && (
          <>
            <Button size="sm" variant="ghost" icon="grid" onClick={() => ui.set({ composer: { slots: 4, group: true } })}>
              Compose group
            </Button>
            <Button size="sm" variant="ghost" icon="layout" onClick={() => ui.set({ panel: 'layouts' })}>
              Layouts
            </Button>
          </>
        )}
      </div>
      {groups.map((g) => {
        const gd = readGroup(g);
        const sessions = groupSessions(doc.nodes, g);
        const links = linkedFrom(ctl, g.id);
        const items: MenuEntry[] = ui
          ? [
              ...groupMenuItems(ctl, ui, g, sessions, { rename: () => setRenaming(g), confirm: (what) => setConfirm({ g, what }) }),
              'sep',
              { type: 'heading', label: 'Link to orchestrator' },
              ...linkItems(ctl, project, g, orchestrators),
            ]
          : [];
        return (
          <div key={g.id} className="wb-bgroup" style={{ ['--group-c' as string]: `var(--tag-${gd.color})` }} role="group" aria-label={`Group ${gd.name}`}>
            <div className="wb-bgroup-head">
              <span className="group-dot" aria-hidden="true" />
              <span className="wb-bgroup-name truncate">{gd.name}</span>
              <span className="wb-bgroup-role">{ROLE_LABEL[gd.role]}</span>
              <span className="spacer" />
              {items.length > 0 && <Menu label={`Actions for group ${gd.name}`} items={items} />}
            </div>
            <ul>{gd.members.map((id) => doc.nodes.find((x) => x.id === id)).flatMap((n) => (n ? [row(n)] : []))}</ul>
            {gd.members.length === 0 && <p className="wb-bgroup-empty text-3">No consoles: add some from its menu.</p>}
            {links.length > 0 && (
              <p className="wb-bgroup-link">
                <span className="wb-wire" aria-hidden="true" />
                Linked to {links.map(cardTitle).join(', ')}
              </p>
            )}
          </div>
        );
      })}
      {loose.length > 0 && (
        <>
          {groups.length > 0 && <h3 className="wb-section-title">Not in a group</h3>}
          <ul>{loose.map(row)}</ul>
        </>
      )}
      {renaming && <RenameGroup ctl={ctl} node={renaming} onClose={() => setRenaming(null)} />}
      {newGroup && <NewGroupDialog ctl={ctl} consoles={consoles} picked={newGroup} onClose={() => setNewGroup(null)} />}
      {confirm?.what === 'dissolve' && (
        <ConfirmDialog title={`Ungroup ${readGroup(confirm.g).name}?`} confirmLabel="Ungroup" icon="ungroup" onClose={() => setConfirm(null)} onConfirm={() => ctl.apply([{ op: 'del', id: confirm.g.id }])}>
          <p>The group goes away; its consoles keep running where they are. Links to the group are removed.</p>
        </ConfirmDialog>
      )}
      {confirm?.what === 'kill' && (
        <ConfirmDialog title={`Kill ${readGroup(confirm.g).name}?`} confirmLabel="Kill" danger onClose={() => setConfirm(null)} onConfirm={() => killSessions(groupSessions(ctl.nodes, confirm.g))}>
          <p>{groupSessions(ctl.nodes, confirm.g).map(nameOf).join(', ')} stop. They stay on the board marked DONE; restart them from their menus.</p>
        </ConfirmDialog>
      )}
    </section>
  );
}

function RenameGroup({ ctl, node, onClose }: { ctl: BoardController; node: BoardNode; onClose: () => void }) {
  const [name, setName] = useState(readGroup(node).name);
  const save = () => {
    const v = name.trim();
    if (v && v !== readGroup(node).name) ctl.updateData(node.id, { name: v });
    onClose();
  };
  return (
    <Modal
      title="Rename group"
      icon="pencil"
      size="sm"
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!name.trim()} onClick={save}>
            Rename
          </Button>
        </>
      }
    >
      <TextField label="Name" value={name} maxLength={24} autoFocus onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && save()} />
    </Modal>
  );
}

function NewGroupDialog({ ctl, consoles, picked, onClose }: { ctl: BoardController; consoles: BoardNode[]; picked: string[]; onClose: () => void }) {
  const [name, setName] = useState(() => nextGroupName(ctl.nodes));
  const [role, setRole] = useState<Role>('general');
  const [ids, setIds] = useState<string[]>(picked);
  const make = () => {
    const made = newGroupOps(ctl.nodes, ids, boardFreeSpot(ctl), { name: name.trim() || undefined, role });
    if (!made) return;
    ctl.apply(made.ops);
    toast.success(`${readGroup(made.group).name} made`);
    onClose();
  };
  return (
    <Modal
      title="New group"
      icon="users"
      size="md"
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!ids.length} onClick={make}>
            Make group
          </Button>
        </>
      }
    >
      <div className="stack">
        <TextField label="Name" value={name} maxLength={24} onChange={(e) => setName(e.target.value)} />
        <label className="field">
          <span className="field-label">Role</span>
          <select className="control" value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {ROLE_LABEL[r]}
              </option>
            ))}
          </select>
        </label>
        <fieldset className="stack-sm">
          <legend className="field-label">Consoles (up to 12)</legend>
          {consoles.map((n) => {
            const s = sessionOf(n);
            return (
              <Checkbox
                key={n.id}
                checked={ids.includes(n.id)}
                disabled={!ids.includes(n.id) && ids.length >= 12}
                onChange={(v) => setIds((cur) => (v ? [...cur, n.id] : cur.filter((x) => x !== n.id)))}
                label={s ? `${nameOf(s)} · ${s.name}` : n.id}
              />
            );
          })}
        </fieldset>
        <p className="setting-hint">They line up together on the board, in a group frame there too.</p>
      </div>
    </Modal>
  );
}

/** The board's cards that open as tabs, and New card. */
export function CardsList({ ws, ctl, onOpen }: { ws: Workspace; ctl: BoardController; onOpen?: () => void }) {
  const doc = useDoc(ctl);
  const ui = useContext(BoardEnvContext)?.ui;
  const cards = doc.nodes.filter((n) => opensAsTab(n.kind));
  const open = (n: BoardNode) => {
    rememberCard(n);
    ws.openTool('card', n.id);
    onOpen?.();
  };
  const add = (kind: (typeof NEW_CARD_KINDS)[number]) => {
    if (!ui) return;
    const n = createTile(ctl, ui, kind, boardFreeSpot(ctl));
    open(n);
  };
  const kinds = [...new Set(cards.map((n) => n.kind))].sort((a, b) => (CARD_KINDS[a]?.label ?? a).localeCompare(CARD_KINDS[b]?.label ?? b));
  return (
    <div className="wb-agents-list wb-cards-list nowheel" aria-label="Cards">
      <div className="wb-board-actions">
        <Menu
          label="New card"
          icon="plus"
          triggerLabel="New card"
          triggerClassName="btn btn-secondary btn-sm"
          items={NEW_CARD_KINDS.map((k): MenuEntry => ({ label: CARD_KINDS[k].label, icon: CARD_KINDS[k].icon, onSelect: () => add(k) }))}
        />
      </div>
      {kinds.map((k) => (
        <section key={k} className="wb-agents-section">
          <h3 className="wb-section-title">{CARD_KINDS[k]?.label ?? k}</h3>
          <ul>
            {cards
              .filter((n) => n.kind === k)
              .map((n) => (
                <li key={n.id} className="wb-agent-row wb-card-row">
                  <Icon name={CARD_KINDS[n.kind]?.icon ?? 'grid'} />
                  <span className="wb-agent-who">
                    <span className="wb-agent-name truncate">{cardTitle(n)}</span>
                  </span>
                  <Button size="sm" variant="ghost" aria-label={`Open ${cardTitle(n)} as a tab`} onClick={() => open(n)}>
                    Open
                  </Button>
                </li>
              ))}
          </ul>
        </section>
      ))}
      {!cards.length && <p className="wb-agents-empty text-3">This board has no card yet. New card adds one here and on the board.</p>}
    </div>
  );
}

/** The forward edges and the orchestrator wires of a console, and its group. */
function linksOf(ctl: BoardController, nodeId: string) {
  const { nodes, edges } = ctl.sync.getState();
  const group = groupNodes(nodes).find((g) => readGroup(g).members.includes(nodeId));
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const directors = edges.filter((e) => e.kind === 'wire' && (e.target === nodeId || e.target === group?.id) && byId.get(e.source)?.kind === 'orchestrator').map((e) => ({ card: byId.get(e.source)!, viaGroup: e.target !== nodeId }));
  const forwards = edges.filter((e) => e.kind === 'forward' && e.source === nodeId).flatMap((e) => (byId.get(e.target) ? [{ edge: e, to: byId.get(e.target)! }] : []));
  return { group, directors, forwards };
}

/** How a console or group is called in a list. */
function nodeLabel(n: BoardNode): string {
  if (n.kind === 'group') return `${readGroup(n).name} (group)`;
  const s = sessionOf(n);
  return s ? nameOf(s) : 'Console';
}

/**
 * A console's menu in the Code view: its board tile's own menu (useConsoleMenu, with its dialogs),
 * where "Forward results to" lists the consoles and groups instead of waiting for a click on the
 * board; then its orchestrator links and its group.
 */
export function BoardConsoleMenu({ ctl, project, nodeId, session, onNewGroup, label }: { ctl: BoardController; project: Project; nodeId: string; session: Session; onNewGroup?: (ids: string[]) => void; label?: string }) {
  const doc = useDoc(ctl);
  const machine = useMachine(session.machineId);
  const actions = useConsoleActions(nodeId, session, machine);
  const [renaming, setRenaming] = useState<'handle' | 'name' | null>(null);
  const [making, setMaking] = useState<string[] | null>(null);
  const { group, forwards } = linksOf(ctl, nodeId);
  const orchestrators = doc.nodes.filter((n) => n.kind === 'orchestrator');
  const consoles = doc.nodes.filter((n) => n.kind === 'terminal' && n.id !== nodeId && sessionOf(n)?.agentKind === 'agent');
  const targets = [...consoles, ...groupNodes(doc.nodes).filter((g) => g !== group)];
  const forwardTo = (to: BoardNode) => {
    const on = forwards.find((f) => f.to.id === to.id);
    if (on) {
      ctl.apply([{ op: 'delEdge', id: on.edge.id }]);
      toast.success('Forward removed');
      return;
    }
    toggleLink(ctl, project, ctl.node(nodeId)!, to);
  };
  const node = doc.nodes.find((n) => n.id === nodeId);
  const board: MenuEntry[] = node
    ? [
        { type: 'heading', label: 'Forward results to' },
        ...(targets.length
          ? targets.map((t): MenuEntry => ({ label: nodeLabel(t), icon: t.kind === 'group' ? 'users' : 'forward', checked: forwards.some((f) => f.to.id === t.id), onSelect: () => forwardTo(t) }))
          : [{ label: 'No other console or group on this board', disabled: true, onSelect: () => undefined } as MenuEntry]),
        'sep',
        { type: 'heading', label: 'Link to orchestrator' },
        ...linkItems(ctl, project, node, orchestrators),
        'sep',
        { type: 'heading', label: 'Group' },
        ...groupNodes(doc.nodes)
          .filter((g) => g !== group)
          .map((g): MenuEntry => ({
            label: `Add to ${readGroup(g).name}`,
            icon: 'users',
            disabled: readGroup(g).members.length >= 12,
            onSelect: () => {
              const ops = addToGroupOps(ctl.nodes, g.id, nodeId);
              if (ops) ctl.apply(ops);
            },
          })),
        { label: 'New group with this console…', icon: 'plus', onSelect: () => (onNewGroup ? onNewGroup([nodeId]) : setMaking([nodeId])) },
      ]
    : [];
  const items = useConsoleMenu(nodeId, session, actions, { startRename: (what) => setRenaming(what === 'handle' && !session.handle ? 'name' : what), forward: board });
  return (
    <>
      <Menu label={label ?? `Actions for ${nameOf(session)}`} items={items} />
      {actions.dialogs}
      {renaming && <RenameConsole session={session} what={renaming} onClose={() => setRenaming(null)} />}
      {making && <NewGroupDialog ctl={ctl} consoles={doc.nodes.filter((n) => n.kind === 'terminal' && sessionOf(n)?.agentKind === 'agent')} picked={making} onClose={() => setMaking(null)} />}
    </>
  );
}

function RenameConsole({ session, what, onClose }: { session: Session; what: 'handle' | 'name'; onClose: () => void }) {
  const [value, setValue] = useState(what === 'handle' ? (session.handle ?? '') : session.name);
  const [error, setError] = useState('');
  const save = async () => {
    const v = value.trim().replace(/^@/, '');
    if (!v || v === (what === 'handle' ? session.handle : session.name)) return onClose();
    if (what === 'handle') {
      const problem = handleProblem(v);
      if (problem) return setError(problem);
    }
    try {
      store.upsertSession(what === 'handle' ? await api.setHandle(session.id, v) : await api.renameSession(session.id, v));
      onClose();
    } catch (err) {
      setError(isCode(err, 'handle_taken') ? `@${v} is already used by another console. Pick another name.` : errorMessage(err));
    }
  };
  return (
    <Modal
      title={what === 'handle' ? 'Rename handle' : 'Rename'}
      icon="pencil"
      size="sm"
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!value.trim()} onClick={() => void save()}>
            Rename
          </Button>
        </>
      }
    >
      <TextField label={what === 'handle' ? 'Handle' : 'Name'} value={value} error={error || undefined} autoFocus onChange={(e) => (setValue(e.target.value), setError(''))} onKeyDown={(e) => e.key === 'Enter' && void save()} />
    </Modal>
  );
}

/**
 * Over a console of the Code view's board: its group, the orchestrator cards that direct it and
 * where it forwards its results, and its menu: what its tile on the board shows.
 */
export function BoardConsoleBar({ session }: { session: Session }) {
  const env = useContext(PaneEnv);
  const ctl = env && env.nodeId === '' ? env.ctl : undefined;
  const doc = useSyncExternalStore(ctl?.sync.subscribe ?? noSubscribe, ctl?.sync.getState ?? noDoc);
  if (!ctl || !doc || !env) return null;
  const node = doc.nodes.find((n) => n.kind === 'terminal' && n.data.sessionId === session.id);
  if (!node) return null;
  const { group, directors, forwards } = linksOf(ctl, node.id);
  const g = group ? readGroup(group) : null;
  return (
    <div className="wb-pane-link wb-console-bar">
      {g && (
        <span className="wb-console-group" style={{ ['--group-c' as string]: `var(--tag-${g.color})` }} title={`In the group ${g.name} (${ROLE_LABEL[g.role]})`}>
          <span className="group-dot" aria-hidden="true" />
          <span className="truncate">{g.name}</span>
        </span>
      )}
      {directors.map(({ card, viaGroup }) => (
        <span key={card.id} className="wb-pane-directed" title={viaGroup ? `${cardTitle(card)} directs ${g?.name ?? 'its group'}` : `${cardTitle(card)} directs this console`}>
          <Icon name="orchestrator" />
          <span className="truncate">Directed by {cardTitle(card)}</span>
        </span>
      ))}
      {forwards.map(({ edge, to }) => (
        <span key={edge.id} className="wb-pane-directed" title={`Its results go to ${nodeLabel(to)}`}>
          <Icon name="forward" />
          <span className="truncate">{nodeLabel(to)}</span>
        </span>
      ))}
      <span className="spacer" />
      <BoardConsoleMenu ctl={ctl} project={env.project} nodeId={node.id} session={session} label={`Console menu of ${nameOf(session)}`} />
    </div>
  );
}
const noSubscribe = () => () => undefined;
const noDoc = () => null;
